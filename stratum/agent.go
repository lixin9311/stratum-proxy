package stratum

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AgentConfig is used to create a new Agent
type AgentConfig struct {
	Name         string
	Diff         float64
	Username     string
	Password     string
	Upstream     string
	ConnTimeout  time.Duration
	AgentTimeout time.Duration
}

type dummy struct {
	Method string `json:"method"`
}

// Agent represents a miner to the upstream
type Agent struct {
	sync.Mutex
	extraNonce       string
	extraNonceLen    int
	targetDiff       float64
	currentJob       *Job
	destroyed        bool
	rwLock           sync.RWMutex
	alias            string
	dec              *json.Decoder
	enc              *json.Encoder
	conf             *AgentConfig
	seq              uint64
	subcribeID       string
	conn             net.Conn
	notification     chan *Request
	respNotification map[uint64](chan *Response)
	cancel           context.CancelFunc
}

// Destroy close the connection and channel, stop the loop
func (agent *Agent) Destroy() {
	agent.Lock()
	defer agent.Unlock()
	agent.cancel()
	agent.conn.Close()
	logger.Debugf("AGENT[%s] -- : Destroyed\n", agent.alias)
	if !agent.destroyed {
		close(agent.notification)
		agent.destroyed = true
	}
}

func (agent *Agent) IsDestroyed() bool {
	agent.Lock()
	defer agent.Unlock()
	return agent.destroyed
}

// GetJob returns a copy of current Job
func (agent *Agent) GetJob() *Job {
	agent.rwLock.RLock()
	defer agent.rwLock.RUnlock()
	if agent.currentJob == nil {
		return nil
	}
	return agent.currentJob.Copy()
}

// GetExtraNonce returns a copy of current extranonce
func (agent *Agent) GetExtraNonce() (extranonce string, length int) {
	agent.rwLock.RLock()
	extranonce = agent.extraNonce
	length = agent.extraNonceLen
	agent.rwLock.RUnlock()
	return
}

// GetTargetDiff returns target difficulty
func (agent *Agent) GetTargetDiff() float64 {
	agent.rwLock.RLock()
	diff := agent.targetDiff
	agent.rwLock.RUnlock()
	return diff
}

// Notify returns the channel needed to receive notifications from the upstream
func (agent *Agent) Notify() chan *Request {
	return agent.notification
}

func (agent *Agent) loop(ctx context.Context) {
	defer agent.Destroy()
	defer logger.Debugf("AGENT[%s] -- : Exit agent loop\n", agent.alias)
	for {
		select {
		case <-ctx.Done():
			return
		default:
			tmpMsg := new(json.RawMessage)
			if err := agent.read(tmpMsg); err != nil {
				logger.Warnf("AGENT[%s] -- : Agent loop crashed: %v\n", agent.alias, err)
				return
			}
			logger.Debugf("AGENT[%s] <- : \"%s\"\n", agent.alias, *tmpMsg)

			if strings.Contains(string(*tmpMsg), "method") {
				// request
				req := new(Request)
				if err := json.Unmarshal(*tmpMsg, req); err != nil {
					logger.Errorf("AGENT[%s] -- : Failed to unmarshal incoming request: %v\n", agent.alias, err)
					continue
				}
				switch req.Method {
				case "mining.set_difficulty":
					logger.Infof("AGENT[%s] -- : Set target difficulty to %.0f\n", agent.alias, req.Params[0].(float64))
					agent.rwLock.Lock()
					agent.targetDiff = req.Params[0].(float64)
					agent.rwLock.Unlock()
				case "mining.set_extranonce":
					agent.rwLock.Lock()
					agent.extraNonce = req.Params[0].(string)
					agent.extraNonceLen = int(req.Params[1].(float64))
					agent.rwLock.Unlock()
				case "mining.notify":
					job, err := NewJobFromArray(req.Params)
					if err != nil || job == nil {
						logger.Errorf("AGENT[%s] -- : Failed to parse Job: %v\n", agent.alias, err)
						continue
					} else {
						agent.rwLock.Lock()
						agent.currentJob = job
						agent.rwLock.Unlock()
					}
				default:
					logger.Warnf("AGENT[%s] -- : dont know how to process method(%s)\n", agent.alias, req.Method)
				}
				// no relay design
				agent.notification <- req

			} else {
				// response
				resp := new(Response)
				if err := json.Unmarshal(*tmpMsg, resp); err != nil {
					logger.Errorf("AGENT[%s] -- : Failed to unmarshal incoming response: %v\n", agent.alias, err)
					continue
				}
				agent.Lock()
				ch, ok := agent.respNotification[resp.ID]
				if !ok {
					agent.Unlock()
					continue
				}
				ch <- resp
				agent.Unlock()
			}
		}
	}
}

// RequestAndResponse sends the Request to the upstream, and waits for the Response
func (agent *Agent) RequestAndResponse(method string, params ...interface{}) (*Response, error) {
	id := agent.seqInc()
	req := NewRequest(&id, method, params...)
	// put the chan first, buffer size 1, so it wont block upon recieving
	ch := make(chan *Response, 1)
	agent.Lock()
	agent.respNotification[id] = ch
	agent.Unlock()
	if err := agent.write(req); err != nil {
		return nil, err
	}
	return agent.getResp(id)
}

// WriteRequstRewriteID rewrites the ID of the Request, then send it to the upstream.
// very useful for submits
func (agent *Agent) WriteRequstRewriteID(req *Request) error {
	*req.ID = agent.seqInc()
	return agent.write(req)
}

func (agent *Agent) getResp(id uint64) (*Response, error) {
	agent.Lock()
	ch, ok := agent.respNotification[id]
	agent.Unlock()
	if !ok {
		return nil, nil
	}
	select {
	case <-time.After(agent.conf.ConnTimeout):
		// timeout
		agent.Lock()
		delete(agent.respNotification, id)
		close(ch)
		agent.Unlock()
		return nil, fmt.Errorf("Getting response for id[%d] timed out", id)
	case resp := <-ch:
		// response
		agent.Lock()
		delete(agent.respNotification, id)
		close(ch)
		agent.Unlock()
		return resp, nil
	}
}

func (agent *Agent) write(v interface{}) error {
	agent.conn.SetWriteDeadline(time.Now().Add(agent.conf.ConnTimeout))
	b, _ := json.Marshal(v)
	logger.Debugf("AGENT[%s] -> : \"%s\"\n", agent.alias, b)
	return agent.enc.Encode(v)
}

func (agent *Agent) writeRaw(b []byte) (int, error) {
	return agent.conn.Write(b)
}

func (agent *Agent) read(v interface{}) error {
	return agent.dec.Decode(v)
}

func (agent *Agent) handshake() error {
	// sub
	subRes, err := agent.RequestAndResponse("mining.subscribe", agent.conf.Name)
	if err != nil {
		return err
	}
	if len(subRes.Error) != 0 {
		return fmt.Errorf("%v", subRes.Error)
	}
	results := new(subscribeResult)
	if err := json.Unmarshal(*subRes.Result, results); err != nil {
		return err
	}
	agent.subcribeID = results.subID
	agent.extraNonce = results.extranonce
	agent.extraNonceLen = results.extranonceLength

	// auth
	authRes, err := agent.RequestAndResponse("mining.authorize", agent.conf.Username, agent.conf.Password)
	if err != nil {
		return err
	}
	if len(authRes.Error) != 0 {
		return fmt.Errorf("%v", authRes.Error)
	}
	authResult := false
	if err := json.Unmarshal(*authRes.Result, &authResult); err != nil {
		return err
	}
	if authResult != true {
		return fmt.Errorf("Authorize failed")
	}

	// extranonce
	nonceRes, err := agent.RequestAndResponse("mining.extranonce.subscribe")
	if err != nil {
		return err
	}
	if len(nonceRes.Error) != 0 {
		return fmt.Errorf("%v", nonceRes.Error)
	}

	return nil
}

func (agent *Agent) seqInc() uint64 {
	return atomic.AddUint64(&agent.seq, 1)
}

// NewAgent creates and initializes a new Agent from the AgentConfig
func NewAgent(conf *AgentConfig) (*Agent, error) {
	agent := new(Agent)
	agent.conf = conf
	// init fields
	agent.alias = strings.Split(conf.Username, ".")[1] + ":" + conf.Password
	agent.notification = make(chan *Request, 10)
	agent.respNotification = map[uint64](chan *Response){}

	// connection
	conn, err := net.DialTimeout("tcp4", agent.conf.Upstream, agent.conf.ConnTimeout)
	if err != nil {
		return nil, err
	}
	agent.conn = conn
	agent.enc = json.NewEncoder(agent.conn)
	agent.dec = json.NewDecoder(agent.conn)
	ctx, cancel := context.WithCancel(context.Background())
	agent.cancel = cancel
	go agent.loop(ctx)
	if err := agent.handshake(); err != nil {
		agent.Destroy()
		return nil, err
	}
	return agent, nil
}

// AgentPool is a thread-safe map used to hold Agents
type AgentPool struct {
	sync.Mutex
	data map[string]*Agent
}

// Get Agents
func (p *AgentPool) Get(key string) (*Agent, bool) {
	p.Lock()
	defer p.Unlock()
	a, ok := p.data[key]
	return a, ok
}

// Put Agents
func (p *AgentPool) Put(key string, agent *Agent) {
	p.Lock()
	defer p.Unlock()
	p.data[key] = agent
}

// Delete Agents
func (p *AgentPool) Delete(key string) {
	p.Lock()
	defer p.Unlock()
	delete(p.data, key)
}

// NewAgentPool creates and initializes a new AgentPool
func NewAgentPool() *AgentPool {
	pool := new(AgentPool)
	pool.data = map[string]*Agent{}
	return pool
}
