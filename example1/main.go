package main

import (
	"context"
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
	"github.com/tarantool/go-tarantool/v2"
)

var (
	tarantoolUser      = "example_user"
	tarantoolPassword  = "example_password"
	tarantoolUri       = "instance001:3301"
	batchSize          = 1000
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
	tarantoolConnect       *tarantool.Connection
	startTimeRPS           int64
	countMessage           int64
	allCountMessage        int64
	reteryCount            int
	tickerSendBatchTimeout *time.Ticker
	startAllRpsTime        int64
}

func Init(ctx context.Context) (*mainApp, error) {
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
		"bootstrap.servers":  "kafka-broker:9092",
		"group.id":           "example1",
		"auto.offset.reset":  "earliest",
		"enable.auto.commit": false,
	})
	if err != nil {
		return nil, err
	}
	err = conKafka.SubscribeTopics([]string{"invalidation"}, nil)
	if err != nil {
		return nil, err
	}

	dialer := tarantool.NetDialer{
		Address:  tarantoolUri,
		User:     tarantoolUser,
		Password: tarantoolPassword,
	}
	opts := tarantool.Opts{}

	conn, err := tarantool.Connect(ctx, dialer, opts)
	if err != nil || conn == nil {
		return nil, err
	}
	return &mainApp{
		requestDurations:       requestDurations,
		tarantoolConnect:       conn,
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
		log.Fatalf("Json parser error: %v (%v)\n", err, msg)
		return nil, err
	}
	id, err := strconv.Atoi(string(msg.Key))
	if err != nil {
		log.Fatalf("Kafka key parser error: %v (%v)\n", err, msg)
		return nil, err
	}
	if id != data.Id {
		log.Fatalf("Kafka key != message id: %v (%v)\n", err, msg)
		return nil, err
	}

	value := string(msg.Value)
	tuple := []interface{}{id, value}
	return tuple, nil
}
func (self *mainApp) processed(tuple []interface{}) error {
	self.countMessage++
	self.allCountMessage++

	return self.send(tuple)
}
func (self *mainApp) send(tuple []interface{}) error {

	startTimeGetMessage := time.Now().UnixNano()
	startTimeGetMessage = time.Now().UnixNano()
	req := tarantool.NewReplaceRequest("test").Tuple(tuple)
	_, err := self.tarantoolConnect.Do(req).Get()
	self.requestDurations.WithLabelValues("sendMessage").Observe(float64(time.Now().UnixNano() - startTimeGetMessage))
	if err != nil {
		return err
	}

	self.conKafka.Commit()
	self.tickerSendBatchTimeout.Reset(sendBatchesTimeout)
	return nil
}

func (self *mainApp) printRPS() {
	stopTimeRPS := time.Now().UnixNano()
	if self.countMessage > 0 {
		time := stopTimeRPS - self.startTimeRPS
		log.Printf("time: %d count: %d allCount: %d rps: %f\n",
			time, self.countMessage, self.allCountMessage, float64(self.countMessage*1e9)/float64(time))
		self.countMessage = 0
	} else {
		log.Printf("No message")
	}
	self.startTimeRPS = stopTimeRPS
}

func main() {
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, os.Interrupt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	app, err := Init(ctx)
	if err != nil {
		log.Fatalf("Error init app: %s\n", err)
		os.Exit(1)
	}
	defer app.conKafka.Close()
	defer app.tarantoolConnect.Close()

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
			tuple, err := app.parseMesage(msg)
			if err != nil {
				log.Fatalf("Error parseMesage message: %s\n", err)
				continue
			}
			err = app.processed(tuple)
			if err != nil {
				log.Fatalf("Error send to tarantool: %s\n", err)
				run = false
			}
			if app.allCountMessage == 1e5 {
				stopRpsTime := time.Now().UnixNano()
				fmt.Printf("Processed 1e7 messages time: %d count: %d rps: %f\n",
					(stopRpsTime - app.startAllRpsTime), app.allCountMessage, float64((app.allCountMessage-1)*1e9)/float64(stopRpsTime-app.startAllRpsTime))
				run = false
			}
		}
	}

	os.Exit(0)
}
