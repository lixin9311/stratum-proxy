package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v2"

	"github.com/fsnotify/fsnotify"
)

type Config struct {
	AgentName        string        `yaml:"AgentName"`
	Upstream         string        `yaml:"Upstream"`
	WorkerDifficulty float64       `yaml:"WorkerDifficulty"`
	DebugLevel       int           `yaml:"DebugLevel"`
	AgentTimeout     time.Duration `yaml:"AgentTimeout"`
	WorkerTimeout    time.Duration `yaml:"WorkerTimeout"`
	ConnTimeout      time.Duration `yaml:"ConnTimeout"`
	SubmitRetry      int           `yaml:"SubmitRetry"`
}

func NewConfigFromFile(path string) (*Config, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	conf := new(Config)
	if err := yaml.Unmarshal(data, conf); err != nil {
		return nil, err
	}
	return conf, nil
}

func CheckConfig(conf *Config) error {
	if conf.AgentName == "" {
		return fmt.Errorf("Empty AgentName")
	}
	if _, _, err := net.SplitHostPort(conf.Upstream); err != nil {
		return fmt.Errorf("Invalid Upstream(%s): %v", conf.Upstream, err)
	}
	if conf.WorkerDifficulty <= 0 {
		return fmt.Errorf("Invalid WorkerDifficulty(%0.f)", conf.WorkerDifficulty)
	}
	if conf.AgentTimeout < 0 {
		return fmt.Errorf("Invalid AgentTimeout(%v)", conf.AgentTimeout)
	}
	if conf.WorkerTimeout < 0 {
		return fmt.Errorf("Invalid WorkerTimeout(%v)", conf.WorkerTimeout)
	}
	if conf.ConnTimeout < 0 {
		return fmt.Errorf("Invalid ConnTimeout(%v)", conf.ConnTimeout)
	}
	if conf.SubmitRetry < 0 {
		return fmt.Errorf("Invalid SubmitRetry(%v)", conf.SubmitRetry)
	}
	return nil
}

func watchConfig(path string) (chan *Config, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := watcher.Add(absPath); err != nil {
		return nil, err
	}
	ch := make(chan *Config)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					close(ch)
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					conf, err := NewConfigFromFile(absPath)
					if err != nil {
						log.Printf("Malformed config file: %v\n", err)
						continue
					}
					ch <- conf
					// process new config
				}
			case <-watcher.Errors:
				close(ch)
				return
			}
		}
	}()
	return ch, nil
}
