package stratum

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// WorkerConfig is used to create a new worker
type WorkerConfig struct {
	WorkerTimeout time.Duration
	ConnTimeout   time.Duration
}

// Worker represents a miner to this proxy
type Worker struct {
	// they won't change after handshake
	Username string
	Password string

	// conf is not thread-safe yet
	conf         *WorkerConfig
	connLock     sync.Mutex
	isDestroyed  bool
	dec          *json.Decoder
	enc          *json.Encoder
	conn         net.Conn
	numOfSubmits uint64
	closeLock    sync.Mutex
	rwLock       sync.RWMutex
	alias        string
	extranonce   string
	workerDiff   float64
	targetDiff   float64
	currentJob   *Job
	notification chan *Request
	cancel       context.CancelFunc
}

// NewWorker creates and *PARTIALLY* initialize the Worker.
// You need to:
//    1. set difficulty
//    2. set extranonce
//    3. set job
func NewWorker(c net.Conn, conf *WorkerConfig) (*Worker, error) {
	w := new(Worker)
	w.conn = c
	w.conf = conf
	w.enc = json.NewEncoder(w.conn)
	w.dec = json.NewDecoder(w.conn)
	w.notification = make(chan *Request, 10)
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	if err := w.handshake(); err != nil {
		w.Destroy()
		return nil, err
	}
	w.alias = strings.Split(w.Username, ".")[1] + ":" + w.Password
	go w.loop(ctx)
	return w, nil
}

// ResetNumOfSubmits returns the num of submits and resets the counter
func (w *Worker) ResetNumOfSubmits() uint64 {
	return atomic.SwapUint64(&w.numOfSubmits, 0)
}

// Notify returns the channel you need to receive submits
func (w *Worker) Notify() chan *Request {
	return w.notification
}

func (w *Worker) read(v interface{}) error {
	w.conn.SetReadDeadline(time.Now().Add(w.conf.WorkerTimeout))
	if err := w.dec.Decode(v); err != nil {
		logger.Debugf("WORKER[%s] -> : Failed to receive msg, \"%v\"\n", w.alias, err)
		return err
	}
	b, _ := json.Marshal(v)
	logger.Debugf("WORKER[%s] -> : \"%s\"\n", w.alias, b)
	return nil
}

func (w *Worker) write(v interface{}) error {
	w.connLock.Lock()
	defer w.connLock.Unlock()
	b, _ := json.Marshal(v)
	logger.Debugf("WORKER[%s] <- : \"%s\"\n", w.alias, b)
	w.conn.SetWriteDeadline(time.Now().Add(w.conf.ConnTimeout))
	return w.enc.Encode(v)
}

// Destroy closes the connection and channels
func (w *Worker) Destroy() {
	w.closeLock.Lock()
	defer w.closeLock.Unlock()
	// avoid closing a closed channel
	if !w.isDestroyed {
		w.isDestroyed = true
		w.cancel()
		w.conn.Close()
		close(w.notification)
	}
	logger.Debugf("WORKER[%s]: Destroyed\n", w.alias)
}

// SetWorkerDifficulty sets the difficulty for the real miner
func (w *Worker) SetWorkerDifficulty(diff float64) error {
	w.rwLock.Lock()
	w.workerDiff = diff
	w.rwLock.Unlock()
	req := NewRequest(nil, "mining.set_difficulty", diff)
	return w.write(req)
}

// SetTargetDifficulty sets the upstream difficulty to the filter
func (w *Worker) SetTargetDifficulty(diff float64) {
	w.rwLock.Lock()
	w.targetDiff = diff
	w.rwLock.Unlock()
}

// SetJob gives the Worker a Job
func (w *Worker) SetJob(job *Job) error {
	w.rwLock.Lock()
	w.currentJob = job
	w.rwLock.Unlock()
	req := NewRequest(nil, "mining.notify", job.ToArray()...)
	return w.write(req)
}

func (w *Worker) getTargetDiff() float64 {
	w.rwLock.RLock()
	defer w.rwLock.RUnlock()
	return w.targetDiff
}

func (w *Worker) getWorkerDiff() float64 {
	w.rwLock.RLock()
	defer w.rwLock.RUnlock()
	return w.workerDiff
}

func (w *Worker) getJob() *Job {
	w.rwLock.RLock()
	defer w.rwLock.RUnlock()
	if w.currentJob == nil {
		return nil
	}
	return w.currentJob.Copy()
}

func (w *Worker) getExtraNonce() string {
	w.rwLock.RLock()
	defer w.rwLock.RUnlock()
	return w.extranonce
}

// SetExtranonce sets the extranonce.
// Be aware, I here assumes the miner have the ability to receive extranonce update
func (w *Worker) SetExtranonce(extranonce string, length int) error {
	w.rwLock.Lock()
	w.extranonce = extranonce
	w.rwLock.Unlock()
	req := NewRequest(nil, "mining.set_extranonce", extranonce, length)
	return w.write(req)
}

func (w *Worker) ack(id uint64, ok bool) error {
	var str string
	if ok {
		str = "true"
	} else {
		str = "false"
	}
	resp := new(Response)
	resp.ID = id
	result := json.RawMessage([]byte(str))
	resp.Result = &result
	return w.write(resp)
}

func (w *Worker) loop(ctx context.Context) {
	defer w.Destroy()
	for {
		select {
		case <-ctx.Done():
			// finish the loop
			return
		default:
			request := new(Request)
			if err := w.read(request); err != nil {
				logger.Warnf("WORKER[%s]: Disconnected, %v\n", w.alias, err)
				return
			}
			switch request.Method {
			case "mining.submit":
				// calculate diff
				// it doesnt have any job
				job := w.getJob()
				if job == nil {
					continue
				}
				id := *request.ID
				diff := getDiff(job, w.getExtraNonce(), request.Params)
				workerDiff := w.getWorkerDiff()
				targetDiff := w.getTargetDiff()
				if diff < workerDiff {
					logger.Debugf("WORKER[%s]: Invalid share diff %.1f with the request diff %.1f.\n", w.alias, diff, workerDiff)
					continue
				} else {
					atomic.AddUint64(&w.numOfSubmits, 1)
					if diff >= targetDiff {
						logger.Debugf("WORKER[%s]: Share diff(%.1f) higher than target diff(%.1f), send it.\n", w.alias, diff, targetDiff)
						w.notification <- request
					}
					if err := w.ack(id, true); err != nil {
						logger.Errorf("WORKER[%s]: Failed to ack msg[%d:mining.submit], %v\n", id, err)
					}
				}
			case "mining.extranonce.subscribe":
				id := *request.ID
				if err := w.ack(id, true); err != nil {
					logger.Errorf("WORKER[%s]: Failed to ack msg[%d:mining.extranonce.subscribe], %v\n", id, err)
				}
				return
			default:
				logger.Warnf("WORKER[%s]: I dont know how to deal with it, method(%s).\n", w.alias, request.Method)
			}
		}
	}
}

func (w *Worker) handshake() error {
	// subscribe
	subReq := new(Request)
	if err := w.read(subReq); err != nil {
		return err
	}
	// reply subid nonce nonce lenght
	resp := new(Response)
	resp.ID = *subReq.ID
	result := json.RawMessage([]byte(`[["mining.notify","stub"],"ab",3]`))
	resp.Result = &result
	if err := w.write(resp); err != nil {
		return err
	}
	// auth
	authReq := new(Request)
	if err := w.read(authReq); err != nil {
		return err
	}
	w.Username = authReq.Params[0].(string)
	w.Password = authReq.Params[1].(string)
	if err := w.ack(*authReq.ID, true); err != nil {
		return err
	}

	// extranonce subscription
	nonceReq := new(Request)
	if err := w.read(nonceReq); err != nil {
		return err
	}
	if err := w.ack(*nonceReq.ID, true); err != nil {
		return err
	}

	return nil
}

// WorkerPool is a thread-safe map used to hold Workers
type WorkerPool struct {
	sync.Mutex
	data map[string]*Worker
}

// Get Workers
func (p *WorkerPool) Get(key string) (*Worker, bool) {
	p.Lock()
	defer p.Unlock()
	w, ok := p.data[key]
	return w, ok
}

// Put Workers
func (p *WorkerPool) Put(key string, worker *Worker) {
	p.Lock()
	defer p.Unlock()
	p.data[key] = worker
}

// Delete Workers
func (p *WorkerPool) Delete(key string) {
	p.Lock()
	defer p.Unlock()
	delete(p.data, key)
}

// Keys returns an array of the keys
func (p *WorkerPool) Keys() []string {
	p.Lock()
	defer p.Unlock()
	result := make([]string, 0, len(p.data))
	for k := range p.data {
		result = append(result, k)
	}
	return result
}

// NewWorkerPool creates and initializes a new WorkerPool
func NewWorkerPool() *WorkerPool {
	pool := new(WorkerPool)
	pool.data = map[string]*Worker{}
	return pool
}
