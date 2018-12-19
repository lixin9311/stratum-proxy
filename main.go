package main

import (
	"dayun/stratum-proxy/stratum"
	"flag"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	_ "net/http/pprof"

	"github.com/Sirupsen/logrus"
)

var (
	globalConfig *Config
	logger       = logrus.StandardLogger()
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

var uptimes = map[string]*time.Time{}
var uptimeLock sync.Mutex

func main() {
	// test()
	configPath := flag.String("c", "config.yaml", "Config file")
	listenPort := flag.String("l", "1234", "listen port")
	debug := flag.Bool("D", false, "debug log")
	flag.Parse()
	go func() {
		logger.Println(http.ListenAndServe(":12349", nil))
	}()
	if *debug {
		logger.SetLevel(logrus.TraceLevel)
	}
	logger.SetFormatter(&logrus.TextFormatter{ForceColors: true})
	config, err := NewConfigFromFile(*configPath)
	if err != nil {
		logger.Fatalln("Failed to load config file: ", err)
	}
	if err = CheckConfig(config); err != nil {
		logger.Fatalln("Invalid config, ", err)
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
	logger.Println("AgentName:\t\t", globalConfig.AgentName)
	logger.Println("Upstream:\t\t", globalConfig.Upstream)
	logger.Println("WorkerDiff:\t\t", globalConfig.WorkerDifficulty)
	logger.Println("DebugLevel:\t\t", globalConfig.DebugLevel)
	logger.Println("AgentTimeout:\t", globalConfig.AgentTimeout)
	logger.Println("ConnTimeout:\t\t", globalConfig.ConnTimeout)
	stratum.SetLogger(logger)
	startReportLoop(time.Minute)
	logger.Println(ListenAndHandle(*listenPort))
}

func startReportLoop(duration time.Duration) {
	go func() {
		for {
			time.Sleep(duration)
			numOfWorker, numOfAgent, numOfSubmits := status.Data()
			status.clearSubmit()
			logger.Println("AgentName:          ", globalConfig.AgentName)
			logger.Println("Upstream:           ", globalConfig.Upstream)
			logger.Println("WorkerDiff:         ", globalConfig.WorkerDifficulty)
			logger.Println("DebugLevel:         ", globalConfig.DebugLevel)
			logger.Println("AgentTimeout:       ", globalConfig.AgentTimeout)
			logger.Println("ConnTimeout:        ", globalConfig.ConnTimeout)
			logger.Println("Worker Alive:       ", numOfWorker)
			logger.Println("Agent Alice:        ", numOfAgent)
			logger.Println("Submits in 1min:    ", numOfSubmits)
			logger.Println("============ individual report ============")
			akeys := aPool.Keys()
			sort.Strings(akeys)
			for _, index := range akeys {
				alias := strings.Split(index, ".")[1]
				agent, _ := aPool.Get(index)
				worker, notOrphan := wPool.Get(index)
				var status string
				if agent.IsDestroyed() {
					status = "zombie"
				} else {
					status = "alive"
				}
				// AGENT[0:x] 1024 alive orphan 0h0m0s
				// AGENT[1:x] 512  dead  normal 0h0m0s WORKER[1:x] 64 1293MH/s 0h0m0s
				var line string
				uptimeLock.Lock()
				aUptime := uptimes["A"+index]
				aUptimeStr := sinceFormat(aUptime)
				if !notOrphan {
					line = fmt.Sprintf("AGENT[%s]\t%.0f\t%s\torphan\t%s", alias, agent.GetTargetDiff(), status, aUptimeStr)
				} else {
					wUptime := uptimes["W"+index]
					mhash := globalConfig.WorkerDifficulty * 16 * float64(worker.ResetNumOfSubmits()) / duration.Seconds()
					line = fmt.Sprintf("AGENT[%s]\t%.0f\t%s\tnormal\t%s\tWORKER[%s]\t%.0f\t%.1fMH/s\t%s", alias, agent.GetTargetDiff(), status, aUptimeStr, alias, globalConfig.WorkerDifficulty, mhash, sinceFormat(wUptime))
				}
				uptimeLock.Unlock()
				logger.Println(line)
			}
			logger.Println("=============== report end ================")
		}
	}()
}

func sinceFormat(from *time.Time) string {
	if from == nil {
		return "0h0m0s"
	}
	du := time.Since(*from)
	sec := int(du.Seconds())
	hrs := sec / 3600
	mins := sec % 3600 / 60
	sec = sec % 60
	return fmt.Sprintf("%dh%dm%ds", hrs, mins, sec)
}

func ListenAndHandle(port string) error {
	logger.Println("Listening on ", port)
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return err
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			logger.Warnln("Failed to accept conn:", err)
			continue
		}
		go handleNewConn(conn)

	}
}

var initializingWorker = map[string]*sync.Mutex{}
var initialLock = sync.Mutex{}

func lock(index string) {
	initialLock.Lock()
	ilock, ok := initializingWorker[index]
	if !ok {
		ilock = new(sync.Mutex)
		initializingWorker[index] = ilock
	}
	initialLock.Unlock()
	ilock.Lock()
}

func unlock(index string) {
	initialLock.Lock()
	ilock, ok := initializingWorker[index]
	if ok {
		delete(initializingWorker, index)
	}
	initialLock.Unlock()
	ilock.Unlock()
}

func handleNewConn(conn net.Conn) {
	// 1. initialize the worker
	worker, err := stratum.NewWorker(conn, &stratum.WorkerConfig{WorkerTimeout: globalConfig.WorkerTimeout, ConnTimeout: globalConfig.ConnTimeout})
	if err != nil {
		logger.Errorln("Failed to handle new worker:", err)
		return
	}

	// 2. put the worker into the pool
	index := worker.Username + ":" + worker.Password
	alias := strings.Split(worker.Username, ".")[1] + ":" + worker.Password
	// Lock until the agent is found or initialized
	// to ensure 1 worker only has 1 agent
	lock(index)

	logger.Infof("WORKER[%s]: registering...\n", alias)
	if w, ok := wPool.Get(index); ok {
		logger.Warnf("WORKER[%s]: Already exists, IsDestroyed: %v, destroy the old one..\n", alias, w.IsDestroyed())
		w.Destroy()
	}
	wPool.Put(index, worker)

	// 3. find or create correspoding agent
	agent, ok := aPool.Get(index)
	if !ok || agent.IsDestroyed() {
		logger.Infof("AGENT[%s] not found, creating new one...\n", alias)
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
				logger.Errorf("Failed to create new AGENT[%s], retry after 10s...\n", alias)
				time.Sleep(10 * time.Second)
				continue
			}
			break
		}

		if err != nil {
			logger.Errorf("Failed to create new AGENT[%s] after 10 retries... Disconnecting the worker...\n", alias)
			worker.Destroy()
			wPool.Delete(index)
			// clean up the lock
			unlock(index)
			return
		}

		// successful
		// seperate the agent consumer
		go func(agent *stratum.Agent, index, alias string) {
			unlock(index)

			status.addAgent()
			uptimeLock.Lock()
			now := time.Now()
			uptimes["A"+index] = &now
			uptimeLock.Unlock()

			defer func() {
				aPool.Delete(index)
				uptimeLock.Lock()
				delete(uptimes, "A"+index)
				uptimeLock.Unlock()
				status.subAgent()
				logger.Warnf("AGENT[%s]: Is dead.\n", alias)
			}()

			notifyCh := agent.Notify()
			for {
				// 1. receive nitification from the upstream
				notify, ok := <-notifyCh
				if !ok {
					// if this channel is closed, it's already destroyed
					// the only exit
					return
				}
				// 2. find the worker
				worker, ok := wPool.Get(index)
				if !ok {
					logger.Debugf("AGENT[%s]: There is no worker for now.\n", alias)
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
						wPool.Delete(index)
						worker.Destroy()
						// return
					}
				case "mining.notify":
					job := agent.GetJob()
					if err := worker.SetJob(job); err != nil {
						wPool.Delete(index)
						worker.Destroy()
						// return
					}
				}

			}
		}(agent, index, alias)
		aPool.Put(index, agent)
	} else {
		logger.Infof("AGENT[%s] found, reunsing.\n", alias)
	}

	// 4. pipe the agent and worker
	pipe(agent, worker, index, alias)
}

// pipe the date, the death of any worker will lead to the return of this function
func pipe(agent *stratum.Agent, worker *stratum.Worker, index, alias string) {
	status.addWorker()
	uptimeLock.Lock()
	now := time.Now()
	uptimes["W"+index] = &now
	uptimeLock.Unlock()
	// Any error leads to the destroy of worker
	defer func() {
		uptimeLock.Lock()
		delete(uptimes, "W"+index)
		uptimeLock.Unlock()
		wPool.Delete(index)
		worker.Destroy()
		status.subWorker()
		logger.Warnf("WORKER[%s]: Is dead.\n", alias)
	}()

	// some initialization works left for worker
	// 1. set difficulty
	if err := worker.SetWorkerDifficulty(globalConfig.WorkerDifficulty); err != nil {
		logger.Errorf("PIPE[%s]: Failed to set worker diff: %v\n", alias, err)
		return
	}
	worker.SetTargetDifficulty(agent.GetTargetDiff())

	// 2. set extranonce
	en, l := agent.GetExtraNonce()
	if err := worker.SetExtranonce(en, l); err != nil {
		logger.Errorf("PIPE[%s]: Failed to set worker extranonce: %v\n", alias, err)
		return
	}

	// 3. give job to it
	job := new(stratum.Job)
	*job = *agent.GetJob()
	job.CleanJobs = true
	if err := worker.SetJob(job); err != nil {
		logger.Errorf("PIPE[%s]: Failed to give worker job: %v\n", alias, err)
		return
	}

	// 4. start the pipeline
	submitCh := worker.Notify()

	// read from worker
	for {
		var err error
		submit, ok := <-submitCh
		// worker or agent is destroyed
		if !ok || agent.IsDestroyed() {
			return
		}
		for i := 0; i < globalConfig.SubmitRetry; i++ {
			err = agent.WriteRequstRewriteID(submit)
			if err != nil {
				logger.Errorf("PIPE[%s]: Failed to submit share to upstream: %v\n", alias, err)
				if err, ok := err.(net.Error); ok && err.Timeout() {
					logger.Warnf("PIPE[%s]: Retry submit[%d]...\n", alias, i+1)
					continue
				} else {
					return
				}
			}
		}
		if err != nil {
			logger.Warnf("PIPE[%s]: Retry failed...\n", alias)
			return
		}
		status.addSubmit()
	}

}
