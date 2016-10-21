package main

import (
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"sort"
	"time"

	"github.com/amir/raidman"
	"github.com/influxdata/influxdb/client/v2"
	"github.com/montanaflynn/stats"
	"github.com/robfig/cron"
)

type Task struct {
	Service  string
	Command  string
	Schedule string
	Ttl      float32
}

type Config struct {
	Tasks []Task
}

func main() {
	schedule := cron.New()

	schedule.AddFunc("0 * * * * *", CheckUAABadLogins)

	schedule.Start()

	waitForExit()
}

type TimeSlice []time.Time

func (p TimeSlice) Len() int {
	return len(p)
}

func (p TimeSlice) Less(i, j int) bool {
	return p[i].Before(p[j])
}

func (p TimeSlice) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

func CheckUAABadLogins() {
	riemann, err := raidman.Dial("tcp", os.Getenv("RIEMANN_ADDRESS"))
	if err != nil {
		log.Printf("Error: %s", err)
		return
	}

	influx, err := client.NewHTTPClient(client.HTTPConfig{
		Addr:     os.Getenv("INFLUX_ADDRESS"),
		Username: os.Getenv("INFLUX_USERNAME"),
		Password: os.Getenv("INFLUX_PASSWORD"),
	})
	if err != nil {
		log.Printf("Error: %s", err)
		return
	}

	query := client.Query{
		Command:  `select derivative(mean(value), 1m) from "cf.uaa.audit_service.user_authentication_failure_count" where time >= now() - 15d group by time(1m), "job"`,
		Database: os.Getenv("INFLUX_DATABASE"),
	}
	results, err := fetch(influx, query)
	if err != nil {
		log.Printf("Error: %s", err)
		return
	}

	now := time.Now()
	offset := now.Sub(now.Truncate(24 * time.Hour))

	days := []time.Time{}
	buckets := map[time.Time]float64{}
	for _, value := range results[0].Series[0].Values {
		delta, _ := value[1].(json.Number).Float64()
		timestamp, _ := time.Parse(time.RFC3339, value[0].(string))
		day := timestamp.Add(offset).Truncate(24 * time.Hour)
		if _, ok := buckets[day]; !ok {
			buckets[day] = 0.0
			days = append(days, day)
		}
		if delta > 0.0 {
			buckets[day] = buckets[day] + delta
		}
	}

	sort.Sort(TimeSlice(days))

	totals := []float64{}
	for _, day := range days[:len(days)-2] {
		totals = append(totals, buckets[day])
	}

	mean, _ := stats.Mean(totals[0:])
	stddev, _ := stats.StandardDeviationSample(totals)
	metric := (buckets[days[len(days)-1]] - mean) / stddev

	log.Printf("Mean: %f", mean)
	log.Printf("Stddev: %f", stddev)
	log.Printf("Value: %f", (buckets[days[len(days)-1]]-mean)/stddev)

	riemann.Send(&raidman.Event{
		Time:    now.Unix(),
		Service: "monitor.uaa.audit_service.user_authentication_failure_count.stddevs",
		Metric:  metric,
	})
}

func fetch(c client.Client, q client.Query) ([]client.Result, error) {
	resp, err := c.Query(q)
	if err != nil {
		return []client.Result{}, err
	}
	if resp.Error() != nil {
		return []client.Result{}, resp.Error()
	}
	return resp.Results, nil
}

func waitForExit() os.Signal {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, os.Kill)
	return <-c
}
