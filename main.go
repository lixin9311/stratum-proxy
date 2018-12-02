package main

import (
	"dayun/stratum-proxy/stratum"
	"flag"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
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
	status       = new(stat)
)

type stat struct {
	sync.Mutex
	numOfWorker  int
	numOfAgent   int
	numOfSubmits int
}

func (s *stat) Data() (numOfWorker, numOfAgent, numOfSubmits int) {
	s.Lock()
	defer s.Unlock()
	return s.numOfWorker, s.numOfAgent, s.numOfSubmits
}

func (s *stat) addWorker() {
	s.Lock()
	defer s.Unlock()
	s.numOfWorker++
}

func (s *stat) addAgent() {
	s.Lock()
	defer s.Unlock()
	s.numOfAgent++
}

func (s *stat) subWorker() {
	s.Lock()
	defer s.Unlock()
	s.numOfWorker--
}

func (s *stat) subAgent() {
	s.Lock()
	defer s.Unlock()
	s.numOfAgent--
}

func (s *stat) addSubmit() {
	s.Lock()
	defer s.Unlock()
	s.numOfSubmits++
}

func (s *stat) clearSubmit() {
	s.Lock()
	defer s.Unlock()
	s.numOfSubmits = 0
}

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
	go func() {
		for {
			time.Sleep(time.Minute)
			printReport()
		}

	}()
	log.Println(ListenAndHandle(*listenPort))
}

func printReport() {
	numOfWorker, numOfAgent, numOfSubmits := status.Data()
	status.clearSubmit()
	globalLogger.Println("AgentName:\t\t", globalConfig.AgentName)
	globalLogger.Println("Upstream:\t\t", globalConfig.Upstream)
	globalLogger.Println("WorkerDiff:\t\t", globalConfig.WorkerDifficulty)
	globalLogger.Println("DebugLevel:\t\t", globalConfig.DebugLevel)
	globalLogger.Println("AgentTimeout:\t", globalConfig.AgentTimeout)
	globalLogger.Println("ConnTimeout:\t\t", globalConfig.ConnTimeout)
	globalLogger.Println("Worker Alive:\t", numOfWorker)
	globalLogger.Println("Agent Alice:\t\t", numOfAgent)
	globalLogger.Println("Submits in 1min:\t", numOfSubmits)
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
	globalLogger.Printf("WORKER[%s] registering...\n", index)
	wPool.Put(index, worker)
	status.addWorker()

	// 3. find or create correspoding agent
	agent, ok := aPool.Get(index)
	if !ok || agent.IsDestroyed() {
		globalLogger.Printf("AGENT[%s] found, creating new one...\n", index)
		agentConf := &stratum.AgentConfig{
			Name:         globalConfig.AgentName,
			Username:     worker.Username,
			Password:     worker.Password,
			Upstream:     globalConfig.Upstream,
			ConnTimeout:  globalConfig.ConnTimeout,
			AgentTimeout: globalConfig.AgentTimeout}

		for i := 0; i < 10; i++ {
			agent, err = stratum.NewAgent(agentConf)
			if err != nil {
				globalLogger.Errorf("Failed to create new AGENT[%s], retry after 10s...\n", index)
				time.Sleep(10 * time.Second)
				continue
			}
			break
		}

		if err != nil {
			globalLogger.Errorf("Failed to create new AGENT[%s] after 10 retries... Disconnecting the worker...\n", index)
			worker.Destroy()
			wPool.Delete(index)
			return
		}

		// successful
		// seperate the agent consumer
		go func(agent *stratum.Agent, alias string) {
			status.addAgent()
			defer func() {
				status.subAgent()
				globalLogger.Warnf("AGENT[%s] : Disconnected.\n", alias)
			}()

			notifyCh := agent.Notify()
			for {
				// 1. receive nitification from the upstream
				notify, ok := <-notifyCh
				if !ok {
					// if this channel is closed, it's already destroyed
					aPool.Delete(alias)
					return
				}
				// 2. find the worker
				worker, ok := wPool.Get(alias)
				if !ok {
					globalLogger.Printf("AGENT[%s]: There is no worker for now.\n", alias)
					continue
				}
				// 3. if there is a worker
				// any error represents the worker might be dead
				switch notify.Method {
				case "mining.set_difficulty":
					worker.SetTargetDifficulty(agent.GetTargetDiff())
				case "mining.set_extranonce":
					en, l := agent.GetExtraNonce()
					if err := worker.SetExtranonce(en, l); err != nil {
						wPool.Delete(alias)
						worker.Destroy()
						return
					}
				case "mining.notify":
					job := agent.GetJob()
					if err := worker.SetJob(job); err != nil {
						wPool.Delete(alias)
						worker.Destroy()
						return
					}
				}

			}
		}(agent, index)

		aPool.Put(index, agent)
	} else {
		globalLogger.Printf("AGENT[%s] found, reunsing.\n", index)
	}

	// 4. pipe the agent and worker
	pipe(agent, worker, index)
}

// pipe the date, the death of any worker will lead to the return of this function
func pipe(agent *stratum.Agent, worker *stratum.Worker, alias string) {
	// Any error leads to the destroy of worker
	defer func() {
		wPool.Delete(alias)
		worker.Destroy()
		status.subWorker()
		globalLogger.Warnf("WORKER[%s]: Disconnected.\n", alias)
	}()

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

	// read from worker
	for {
		submit, ok := <-submitCh
		// worker or agent is destroyed
		if !ok || agent.IsDestroyed() {
			return
		}
		if err := agent.WriteRequstRewriteID(submit); err != nil {
			globalLogger.Errorf("PIPE[%s] : Failed to submit share to upstream: %v", alias, err)
			return
		}
		status.addSubmit()
	}

}
