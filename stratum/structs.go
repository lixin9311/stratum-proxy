package stratum

import (
	"fmt"

	"github.com/json-iterator/go"
)

// Request :
//    * message ID - integer or string
//    * remote method - unicode string
//    * parameters - list of parameters
type Request struct {
	ID     *uint64       `json:"id"` // i need it to be null sometimes
	Method string        `json:"method"`
	Params []interface{} `json:"params"`
}

// NewRequest assembles a Request from given parameters
func NewRequest(id *uint64, method string, params ...interface{}) *Request {
	return &Request{ID: id, Method: method, Params: params}
}

// Response :
//    * message ID - same ID as in request, for pairing request-response together
//    * result - any json-encoded result object (number, string, list, array, …)
//    * error - null or list (error code, error message)
// Notification is like Request, but it does not expect any response and message ID is always null
type Response struct {
	ID     uint64               `json:"id"`
	Result *jsoniter.RawMessage `json:"result"`
	Error  []interface{}        `json:"error"`
}

type subscribeResult struct {
	method           string
	subID            string
	extranonce       string
	extranonceLength int
}

func (r *subscribeResult) UnmarshalJSON(b []byte) (err error) {
	var tmp []interface{}
	if err := json.Unmarshal(b, &tmp); err != nil {
		return err
	}
	defer func() {
		if re := recover(); re != nil {
			err = fmt.Errorf("JSON unmarshal paniced: %s", re)
		}
	}()
	if len(tmp) != 0 {
		r.method = tmp[0].([]interface{})[0].(string)
		r.subID = tmp[0].([]interface{})[1].(string)
		r.extranonce = tmp[1].(string)
		r.extranonceLength = int(tmp[2].(float64))
	}
	return err
}

func (r *subscribeResult) MarshalJSON() ([]byte, error) {
	tmp := make([]interface{}, 3)
	tmp[0] = [2]interface{}{r.method, r.subID}
	tmp[1] = r.extranonce
	tmp[2] = r.extranonceLength
	return json.Marshal(tmp)
}

// Job is the struct of an hash job given by the upstream
type Job struct {
	JobID        string
	PrevHash     string
	Coinb1       string
	Coinb2       string
	MerkleBranch []string
	Version      string
	NBits        string
	NTime        string
	CleanJobs    bool
}

// NewJobFromArray serilize a Job struct from an array
func NewJobFromArray(v []interface{}) (*Job, error) {
	var err error
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("Failed to parse Job from array: %v", r)
		}
	}()
	job := new(Job)
	job.JobID = v[0].(string)
	job.PrevHash = v[1].(string)
	job.Coinb1 = v[2].(string)
	job.Coinb2 = v[3].(string)
	branchIf := v[4].([]interface{})
	job.MerkleBranch = make([]string, len(branchIf))
	for i, item := range branchIf {
		job.MerkleBranch[i] = item.(string)
	}
	job.Version = v[5].(string)
	job.NBits = v[6].(string)
	job.NTime = v[7].(string)
	job.CleanJobs = v[8].(bool)
	return job, err
}

// ToArray converts a Job to an array, to calculate the difficulty
func (j *Job) ToArray() []interface{} {
	return []interface{}{j.JobID, j.PrevHash, j.Coinb1, j.Coinb2, j.MerkleBranch,
		j.Version, j.NBits, j.NTime, j.CleanJobs}
}

// Copy returns a deep copy of job
func (j *Job) Copy() *Job {
	result := new(Job)
	*result = *j
	result.MerkleBranch = make([]string, len(j.MerkleBranch))
	for i := range result.MerkleBranch {
		result.MerkleBranch[i] = j.MerkleBranch[i]
	}
	return j
}
