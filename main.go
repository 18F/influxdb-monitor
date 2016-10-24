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

func main() {
	schedule := cron.New()

	registerDerivativeAggregator(
		schedule,
		"0 0 * * * *",
		`select derivative(mean(value), 1m) from "cf.uaa.audit_service.user_authentication_failure_count" where time >= now() - 15d group by time(1m), "job"`,
		"monitor.uaa.audit_service.user_authentication_failure_count.stddevs",
		2*60*60,
	)
	registerDerivativeAggregator(
		schedule,
		"0 0 * * * *",
		`select derivative(mean(value), 1m) from "cf.uaa.audit_service.user_not_found_count" where time >= now() - 15d group by time(1m), "job"`,
		"monitor.uaa.audit_service.user_not_found_count.stddevs",
		2*60*60,
	)
	registerDerivativeAggregator(
		schedule,
		"0 0 * * * *",
		`select derivative(mean(value), 1m) from "cf.uaa.audit_service.user_password_changes" where time >= now() - 15d group by time(1m), "job"`,
		"monitor.uaa.audit_service.user_password_changes.stddevs",
		2*60*60,
	)
	registerDerivativeAggregator(
		schedule,
		"0 0 * * * *",
		`select derivative(mean(value), 1m) from "cf.cc.http_status.4XX" where time >= now() - 15d group by time(1m), "index", "ip", "job"`,
		"monitor.cc.http_status.4xx.stddevs",
		2*60*60,
	)
	registerDerivativeAggregator(
		schedule,
		"0 0 * * * *",
		`select derivative(mean(value), 1m) from "cf.cc.http_status.5XX" where time >= now() - 15d group by time(1m), "index", "ip", "job"`,
		"monitor.cc.http_status.5xx.stddevs",
		2*60*60,
	)

	registerAggregator(
		schedule,
		"0 0 * * * *",
		`select mean(value) from "cf.bbs.LRPsRunning" where time >= now() - 15d and time < now() group by time(1d, now())`,
		"monitor.bbs.LRPsRunning.stddevs",
		2*60*60,
	)
	registerAggregator(
		schedule,
		"0 0 * * * *",
		`select mean(value) from "cf.bbs.TasksRunning" where time >= now() - 15d and time < now() group by time(1d, now())`,
		"monitor.bbs.TasksRunning.stddevs",
		2*60*60,
	)

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

func registerDerivativeAggregator(
	schedule *cron.Cron,
	spec string,
	command string,
	service string,
	ttl float32,
) {
	schedule.AddFunc(spec, func() {
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
			Command:  command,
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
		for _, series := range results[0].Series {
			for _, value := range series.Values {
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
		}

		log.Printf("Buckets: %v", buckets)

		sort.Sort(TimeSlice(days))

		totals := []float64{}
		for _, day := range days[:len(days)-2] {
			totals = append(totals, buckets[day])
		}

		mean, _ := stats.Mean(totals[0:])
		stddev, _ := stats.StandardDeviationSample(totals)
		metric := (buckets[days[len(days)-1]] - mean) / stddev

		log.Printf("Service: %s", service)
		log.Printf("Mean: %f", mean)
		log.Printf("Stddev: %f", stddev)
		log.Printf("Value: %f", (buckets[days[len(days)-1]]-mean)/stddev)

		err = riemann.Send(&raidman.Event{
			Time:    now.Unix(),
			Service: service,
			Metric:  metric,
			Ttl:     ttl,
		})
		if err != nil {
			log.Printf("Error: %s", err)
		}
	})
}

func registerAggregator(
	schedule *cron.Cron,
	spec string,
	command string,
	service string,
	ttl float32,
) {
	schedule.AddFunc(spec, func() {
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
			Command:  command,
			Database: os.Getenv("INFLUX_DATABASE"),
		}
		results, err := fetch(influx, query)
		if err != nil {
			log.Printf("Error: %s", err)
			return
		}

		now := time.Now()

		days := []time.Time{}
		buckets := map[time.Time]float64{}
		for _, series := range results[0].Series {
			for _, value := range series.Values {
				metric, _ := value[1].(json.Number).Float64()
				timestamp, _ := time.Parse(time.RFC3339, value[0].(string))
				buckets[timestamp] = metric
				days = append(days, timestamp)
			}
		}

		log.Printf("Buckets: %v", buckets)

		sort.Sort(TimeSlice(days))

		totals := []float64{}
		for _, day := range days[:len(days)-2] {
			totals = append(totals, buckets[day])
		}

		mean, _ := stats.Mean(totals[0:])
		stddev, _ := stats.StandardDeviationSample(totals)
		metric := (buckets[days[len(days)-1]] - mean) / stddev

		log.Printf("Service: %s", service)
		log.Printf("Mean: %f", mean)
		log.Printf("Stddev: %f", stddev)
		log.Printf("Value: %f", (buckets[days[len(days)-1]]-mean)/stddev)

		err = riemann.Send(&raidman.Event{
			Time:    now.Unix(),
			Service: service,
			Metric:  metric,
			Ttl:     ttl,
		})
		if err != nil {
			log.Printf("Error: %s", err)
		}
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
