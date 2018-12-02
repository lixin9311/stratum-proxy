package main

import (
	"dayun/stratum-proxy/stratum"
	"flag"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	_ "net/http/pprof"

	"github.com/Sirupsen/logrus"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

var (
	globalConfig *Config
	globalLogger = logrus.StandardLogger()
	wPool        = stratum.NewWorkerPool()
	aPool        = stratum.NewAgentPool()
)

func main() {
	// test()
	configPath := flag.String("c", "config.yaml", "Config file")
	listenPort := flag.String("l", "1234", "listen port")
	debug := flag.Bool("D", false, "debug log")
	flag.Parse()
	go func() {
		log.Println(http.ListenAndServe(":12349", nil))
	}()
	if *debug {
		globalLogger.SetLevel(logrus.TraceLevel)
	}
	globalLogger.SetFormatter(&logrus.TextFormatter{ForceColors: true})
	config, err := NewConfigFromFile(*configPath)
	if err != nil {
		globalLogger.Fatalln("Failed to load config file: ", err)
	}
	if err = CheckConfig(config); err != nil {
		globalLogger.Fatalln("Invalid config, ", err)
	}
	globalConfig = config
	// TODO: config update
	// confNotify, err := watchConfig(*configPath)
	// if err != nil {
	// 	log.Println("Cannot watch for config update, config will remain unchanged.")
	// } else {
	// 	go func() {
	// 		<-confNotify
	// 	}()
	// }
	globalLogger.Println("AgentName:\t\t", globalConfig.AgentName)
	globalLogger.Println("Upstream:\t\t", globalConfig.Upstream)
	globalLogger.Println("WorkerDiff:\t\t", globalConfig.WorkerDifficulty)
	globalLogger.Println("DebugLevel:\t\t", globalConfig.DebugLevel)
	globalLogger.Println("AgentTimeout:\t", globalConfig.AgentTimeout)
	globalLogger.Println("ConnTimeout:\t\t", globalConfig.ConnTimeout)
	stratum.SetLogger(globalLogger)
	log.Println(ListenAndHandle(*listenPort))
}

func ListenAndHandle(port string) error {
	globalLogger.Println("Listening on ", port)
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return err
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			globalLogger.Warnln("Failed to accept conn:", err)
			continue
		}
		go handleNewConn(conn)

	}
}

func handleNewConn(conn net.Conn) {
	// 1. initialize the worker
	worker, err := stratum.NewWorker(conn)
	if err != nil {
		globalLogger.Errorln("Failed to handle new worker:", err)
		return
	}

	// 2. put the worker into the pool
	index := strings.Split(worker.Username, ".")[1] + ":" + worker.Password
	globalLogger.Println("New worker registering: ", index)
	wPool.Put(index, worker)

	// 3. find or create correspoding agent
	agent, ok := aPool.Get(index)
	if !ok || agent.IsDestroyed() {
		globalLogger.Println("No agent found, creating new one...")
		agentConf := &stratum.AgentConfig{
			Name:         globalConfig.AgentName,
			Diff:         globalConfig.WorkerDifficulty,
			Username:     worker.Username,
			Password:     worker.Password,
			Upstream:     globalConfig.Upstream,
			ConnTimeout:  globalConfig.ConnTimeout,
			AgentTimeout: globalConfig.AgentTimeout}

		for i := 0; i < 10; i++ {
			agent, err = stratum.NewAgent(agentConf)
			if err != nil {
				globalLogger.Errorln("Failed to create new agent, retry after 10s...")
				time.Sleep(10 * time.Second)
				continue
			}
			break
		}

		if err != nil {
			globalLogger.Errorln("Failed to create new agent, after 10 retries...")
			wPool.Delete(index)
			worker.Destroy()
			return
		}

		aPool.Put(index, agent)
	} else {
		globalLogger.Println("Existing agent found")
	}

	// 4. pipe the agent and worker
	pipe(agent, worker, index)
}

// TODO: apply agent timeout
func pipe(agent *stratum.Agent, worker *stratum.Worker, alias string) {
	defer wPool.Delete(alias)
	defer worker.Destroy()
	// some initialization works left for worker
	// 1. set difficulty
	if err := worker.SetWorkerDifficulty(globalConfig.WorkerDifficulty); err != nil {
		globalLogger.Errorf("PIPE[%s]: Failed to set worker diff: %v\n", alias, err)
		return
	}
	worker.SetTargetDifficulty(agent.GetTargetDiff())

	// 2. set extranonce
	en, l := agent.GetExtraNonce()
	if err := worker.SetExtranonce(en, l); err != nil {
		globalLogger.Errorf("PIPE[%s]: Failed to set worker extranonce: %v\n", alias, err)
		return
	}

	// 3. give job to it
	job := new(stratum.Job)
	*job = *agent.GetJob()
	job.CleanJobs = true
	if err := worker.SetJob(job); err != nil {
		globalLogger.Errorf("PIPE[%s]: Failed to give worker job: %v\n", alias, err)
		return
	}

	// 4. start the pipeline
	submitCh := worker.Notify()
	notifyCh := agent.Notify()

	// TODO: if the worker is offline, avoid some error
	go func() {
		for {
			notify, ok := <-notifyCh
			if !ok {
				return
			}
			switch notify.Method {
			case "mining.set_difficulty":
				worker.SetTargetDifficulty(agent.GetTargetDiff())
			case "mining.set_extranonce":
				en, l := agent.GetExtraNonce()
				if err := worker.SetExtranonce(en, l); err != nil {
					return
				}
			case "mining.notify":
				job := agent.GetJob()
				if err := worker.SetJob(job); err != nil {
					return
				}
			}
		}
	}()

	for {
		submit, ok := <-submitCh
		if !ok {
			return
		}
		if err := agent.WriteRequstRewriteID(submit); err != nil {
			globalLogger.Errorf("PIPE[%s] : Failed to submit share to upstream: %v", alias, err)
			agent.Destroy()
			return
		}
	}

}
