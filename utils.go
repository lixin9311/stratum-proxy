package main

import (
	"fmt"
	"sync"
	"time"
)

var (
	uptimes    = map[string]*time.Time{}
	uptimeLock sync.Mutex

	initializingWorker = map[string]*sync.Mutex{}
	initialLock        = sync.Mutex{}
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
	if ok {
		ilock.Unlock()
	}
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
