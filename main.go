package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/amir/raidman"
	"github.com/influxdata/influxdb/client/v2"
	"github.com/influxdata/influxdb/models"
	"github.com/robfig/cron"
	"gopkg.in/yaml.v2"
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
	influx, err := client.NewHTTPClient(client.HTTPConfig{
		Addr:     os.Getenv("INFLUX_ADDRESS"),
		Username: os.Getenv("INFLUX_USERNAME"),
		Password: os.Getenv("INFLUX_PASSWORD"),
	})
	fatal(err)

	raw, err := ioutil.ReadFile("config.yml")
	fatal(err)

	config := Config{}
	err = yaml.Unmarshal(raw, &config)
	fatal(err)

	schedule := cron.New()

	for _, task := range config.Tasks {
		log.Printf("Registering task: %s", task.Service)
		registerTask(schedule, influx, task)
	}

	schedule.Start()

	waitForExit()
}

func fatal(err error) {
	if err != nil {
		log.Fatalf("Error: %s", err)
	}
}

func registerTask(schedule *cron.Cron, influx client.Client, task Task) {
	schedule.AddFunc(task.Schedule, func() {
		riemann, err := raidman.Dial("tcp", os.Getenv("RIEMANN_ADDRESS"))
		if err != nil {
			log.Printf("Error: %s", err)
			return
		}

		query := client.Query{
			Command:  task.Command,
			Database: os.Getenv("INFLUX_DATABASE"),
		}
		results, err := fetch(influx, query)
		if err != nil {
			log.Printf("Error: %s", err)
			return
		}

		batch := time.Now().String()

		events := []*raidman.Event{}
		for _, result := range results {
			for _, row := range result.Series {
				for _, value := range row.Values {
					timestamp, metric, attributes := formatValue(row, value)
					attributes["batch"] = batch
					log.Printf("metric: %v", metric)
					log.Printf("attributes: %v", attributes)
					events = append(events, &raidman.Event{
						Time:       timestamp,
						Metric:     metric,
						Attributes: attributes,
						Service:    task.Service,
						Ttl:        task.Ttl,
					})
				}
			}
		}

		err = riemann.SendMulti(events)
		if err != nil {
			log.Printf("Error: %s", err)
		}

		log.Printf("Emitted %d events", len(events))
	})
}

func formatValue(row models.Row, value []interface{}) (int64, interface{}, map[string]string) {
	var timestamp int64
	var metric interface{}
	attributes := map[string]string{}
	for idx, name := range row.Columns {
		if name == "time" {
			parsed, err := parseTime(value[idx])
			if err == nil {
				timestamp = parsed
			} else {
				attributes[name] = fmt.Sprintf("%v", value[idx])
			}
		} else if name == "metric" {
			parsed, err := parseMetric(value[idx])
			if err == nil {
				metric = parsed
			} else {
				attributes[name] = fmt.Sprintf("%v", value[idx])
			}
		} else {
			attributes[name] = fmt.Sprintf("%v", value[idx])
		}
	}
	return timestamp, metric, attributes
}

func parseMetric(value interface{}) (interface{}, error) {
	switch value.(type) {
	case json.Number:
		return value.(json.Number).Float64()
	default:
		return nil, fmt.Errorf("Value %v has wrong type", value)
	}
}

func parseTime(value interface{}) (int64, error) {
	switch value.(type) {
	case string:
		parsed, err := time.Parse(time.RFC3339, value.(string))
		if err != nil {
			return 0.0, err
		}
		return parsed.Unix(), nil
	default:
		return 0.0, fmt.Errorf("Value %v has wrong type", value)
	}
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
