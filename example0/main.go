package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	sendBatchesTimeout = 10 * time.Millisecond
	readTimeout        = 5 * time.Millisecond
)

type message struct {
	Id   int   `json:"id"`
	Time int64 `json:"Time"`
}

type mainApp struct {
	requestDurations       *prometheus.SummaryVec
	conKafka               *kafka.Consumer
	startTimeRPS           int64
	countMessage           int64
	allCountMessage        int64
	reteryCount            int
	tickerSendBatchTimeout *time.Ticker
	startAllRpsTime        int64
}

func Init() (*mainApp, error) {
	reg := prometheus.NewRegistry()
	requestDurations := prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:       "request_durations_seconds",
			Help:       "latency distributions.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		[]string{"service"},
	)

	reg.MustRegister(requestDurations)
	reg.MustRegister(
		collectors.NewGoCollector(
			collectors.WithGoCollectorRuntimeMetrics(
				collectors.GoRuntimeMetricsRule{Matcher: regexp.MustCompile("/cpu/classes/idle:cpu-seconds")},
				collectors.GoRuntimeMetricsRule{Matcher: regexp.MustCompile("/cpu/classes/total:cpu-seconds")},
				collectors.GoRuntimeMetricsRule{Matcher: regexp.MustCompile("/cpu/classes/user:cpu-seconds")},
			),
		),
	)

	http.Handle("/metrics", promhttp.HandlerFor(
		reg,
		promhttp.HandlerOpts{
			// Opt into OpenMetrics to support exemplars.
			EnableOpenMetrics: true,
		},
	))

	conKafka, err := kafka.NewConsumer(&kafka.ConfigMap{
		"bootstrap.servers": "kafka-broker:9092",
		"group.id":          "example0",
		"auto.offset.reset": "earliest",
	})
	if err != nil {
		return nil, err
	}
	err = conKafka.SubscribeTopics([]string{"invalidation"}, nil)
	if err != nil {
		return nil, err
	}

	return &mainApp{
		requestDurations:       requestDurations,
		conKafka:               conKafka,
		startTimeRPS:           time.Now().UnixNano(),
		countMessage:           0,
		allCountMessage:        0,
		startAllRpsTime:        0,
		tickerSendBatchTimeout: time.NewTicker(sendBatchesTimeout),
	}, nil
}

func (self *mainApp) readMessage() (*kafka.Message, error) {
	startTimeGetMessage := time.Now().UnixNano()
	msg, err := self.conKafka.ReadMessage(readTimeout)
	self.requestDurations.WithLabelValues("ReadMessage").Observe(float64(time.Now().UnixNano() - startTimeGetMessage))
	return msg, err
}

func (self *mainApp) parseMesage(msg *kafka.Message) ([]interface{}, error) {
	var data message
	err := json.Unmarshal(msg.Value, &data)
	if err != nil {
		panic(fmt.Sprintf("Json parser error: %v (%v)\n", err, msg))
	}
	id, err := strconv.Atoi(string(msg.Key))
	if err != nil {
		panic(fmt.Sprintf("Kafka key parser error: %v (%v)\n", err, msg))
	}
	if id != data.Id {
		panic(fmt.Sprintf("Kafka key != message id: %v (%v)\n", err, msg))
	}

	value := string(msg.Value)
	tuple := []interface{}{id, nil, value}
	self.countMessage++
	self.allCountMessage++
	return tuple, nil
}

func (self *mainApp) printRPS() {
	stopTimeRPS := time.Now().UnixNano()
	if self.countMessage > 0 {
		time := stopTimeRPS - self.startTimeRPS
		log.Printf("time: %d count: %d rps: %f\n",
			time, self.countMessage, float64(self.countMessage*1e9)/float64(time))
		self.countMessage = 0
	} else {
		log.Printf("No message")
	}
	self.startTimeRPS = stopTimeRPS
}

func main() {
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, os.Interrupt)

	app, err := Init()
	if err != nil {
		log.Fatalf("Error init app: %s\n", err)
		os.Exit(1)
	}
	defer app.conKafka.Close()

	go func() {
		log.Println("Start http server for metrics!")
		err = http.ListenAndServe(":7081", nil)
		if err != nil {
			log.Fatalf("Error start http server: %s", err)
			os.Exit(1)
		}
	}()

	log.Println("Run read message!")
	run := true
	tickerRPS := time.NewTicker(time.Second)

	for run {
		select {
		case sig := <-sigchan:
			log.Printf("Caught signal %v: terminating\n", sig)
			run = false
		case <-tickerRPS.C:
			app.printRPS()
		default:
			msg, err := app.readMessage()
			if err != nil {
				if !err.(kafka.Error).IsTimeout() {
					log.Fatalf("Kafka error read message: %s\n", err)
					run = false
				}
				continue
			}
			if app.allCountMessage == 0 { // Игнорим время первого сообшения
				app.startAllRpsTime = time.Now().UnixNano()
			}
			_, err = app.parseMesage(msg)
			if err != nil {
				log.Fatalf("Error parseMesage message: %s\n", err)
				continue
			}

			if app.allCountMessage == 1e7 {
				stopRpsTime := time.Now().UnixNano()
				fmt.Printf("Processed 1e7 messages time: %d count: %d rps: %f\n",
					(stopRpsTime - app.startAllRpsTime), app.allCountMessage, float64((app.allCountMessage-1)*1e9)/float64(stopRpsTime-app.startAllRpsTime))
				run = false
			}
		}
	}

	os.Exit(0)
}
