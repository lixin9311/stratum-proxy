package stratum

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"os"

	"github.com/bitgoin/lyra2rev2"
)

// TODO: logger

var logger LeveledLogger

func init() {
	logger = &defaultLogger{4}
}

type defaultLogger struct {
	level int
}

func (l *defaultLogger) Fatalf(format string, args ...interface{}) {
	if l.level < 1 {
		return
	}
	log.Output(2, fmt.Sprintf(format, args...))
	os.Exit(1)
}

func (l *defaultLogger) Errorf(format string, args ...interface{}) {
	if l.level < 2 {
		return
	}
	log.Output(2, fmt.Sprintf(format, args...))
}

func (l *defaultLogger) Warnf(format string, args ...interface{}) {
	if l.level < 3 {
		return
	}
	log.Output(2, fmt.Sprintf(format, args...))
}

func (l *defaultLogger) Infof(format string, args ...interface{}) {
	if l.level < 4 {
		return
	}
	log.Output(2, fmt.Sprintf(format, args...))
}

func (l *defaultLogger) Debugf(format string, args ...interface{}) {
	if l.level < 5 {
		return
	}
	log.Output(2, fmt.Sprintf(format, args...))
}

func (l *defaultLogger) Tracef(format string, args ...interface{}) {
	if l.level < 6 {
		return
	}
	log.Output(2, fmt.Sprintf(format, args...))
}

// LeveledLogger is an interface of an leveled logger
type LeveledLogger interface {
	Fatalf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Infof(format string, args ...interface{}) // info level
	Debugf(format string, args ...interface{})
	Tracef(format string, args ...interface{})
}

// SetLogger sets the logger for this package
func SetLogger(l LeveledLogger) {
	logger = l
}

func getDiff(job *Job, extranonce1 string, share interface{}) float64 {
	// share [worker_name, jobid, extranonce2, ntime, nonce]
	// coinbase = Coinb1 + Extranonce1 + Extranonce2 + Coinb2
	shareStr := make([]string, 5)
	shareSlice := share.([]interface{})
	for i, item := range shareSlice {
		shareStr[i] = item.(string)
	}

	coinbase := job.Coinb1 + extranonce1 + shareStr[2] + job.Coinb2
	coinbaseBin, _ := hex.DecodeString(coinbase)
	coinbaseHashBin := doubleSha(coinbaseBin)
	merkleRoot := coinbaseHashBin
	for _, h := range job.MerkleBranch {
		bBranch, _ := hex.DecodeString(h)
		merkleRootBin := append(merkleRoot, bBranch...)
		merkleRoot = doubleSha(merkleRootBin)
	}
	bVersion, _ := hex.DecodeString(job.Version)
	bPrevHash, _ := hex.DecodeString(job.PrevHash)
	bNTime, _ := hex.DecodeString(job.NTime)
	bNBits, _ := hex.DecodeString(job.NBits)
	bNonce, _ := hex.DecodeString(shareStr[4])
	header := append(reverseByByte(bVersion), reverse(bPrevHash)...)
	header = append(header, (merkleRoot)...)
	header = append(header, reverseByByte(bNTime)...)
	header = append(header, reverseByByte(bNBits)...)
	header = append(header, reverseByByte(bNonce)...)
	headerHashBin, err := lyra2rev2.Sum(header)
	if err != nil {
		logger.Fatalf("%v\n", err)
	}
	reverseByByte(headerHashBin)

	hashed := big.NewInt(0).SetBytes(headerHashBin)
	hashedf := big.NewFloat(0).SetInt(hashed)

	// 1 << 232
	wtf := big.NewInt(1)
	wtf = wtf.Lsh(wtf, 232)
	wtff := big.NewFloat(0).SetInt(wtf)

	diff := big.NewFloat(0).Quo(wtff, hashedf)

	fl, _ := diff.Float64()

	return fl
}

func doubleSha(b []byte) []byte {
	s1 := sha256.Sum256(b)
	s2 := sha256.Sum256(s1[:])
	return s2[:]
}

func reverse(b []byte) []byte {
	if len(b) > 4 {
		reverse(b[:4])
		reverse(b[4:])
	} else if len(b) == 4 {
		for i := 0; i < len(b)/2; i++ {
			j := len(b) - i - 1
			b[i], b[j] = b[j], b[i]
		}
	}
	return b
}

func reverseByByte(b []byte) []byte {
	for i := 0; i < len(b)/2; i++ {
		j := len(b) - i - 1
		b[i], b[j] = b[j], b[i]
	}
	return b
}

func test() {
	// hash : 00000282888324d9bad32b9483073826e2bfbf4b302b5f30ce93e5e2eeec9307
	extranonce := "65"
	job := new(Job)
	job.JobID = "000000b2ef02c113"
	job.PrevHash = "9505432d9b65ebdb26816464669d60f608aa0792fc94ffca290e8f85f2e7ff6d"
	job.Coinb1 = "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff1203a2ec0f043785015c080000b7bb"
	job.Coinb2 = "0000000002fbe103950000000017a91409a5fc0dc23e9d7206f4da0ee2748a50fca4a1d9870000000000000000266a24aa21a9ed0cafdc4a50b45a7de1c3d11d4872c7986fe63a1412a8d4135e971028284f0bc200000000"
	job.MerkleBranch = []string{"14bd5f9fdd8a24453da71687dc86d003b957799278878d94115069eae52b022b", "0c6ea81a1a1441b3b1455f981a577626bbc8bbbb02a16240aed5ab14db838cb5"}
	job.Version = "20000000"
	job.NBits = "1a68827b"
	job.NTime = "5c018537"
	share := []interface{}{"1DjiVbiJnvcyRSmpJHbJZX4eXwdA3ezkAo.999", "000000b2ef02c113", "000000", "5c018537", "d8f60200"}
	fmt.Println(getDiff(job, extranonce, share))

	input, _ := hex.DecodeString("000000202d430595dbeb659b64648126f6609d669207aa08caff94fc858f0e296dffe7f2cd9d8101ed3e96bda4a9aa27b686aa70d3f73a3a0c4e8ae097ccaf47c3c9d7663785015c7b82681a0002f6d8")
	// 0000034e47d8cceedfef013b1de9c1797ea662a964119d3bc3bac49b9382a001
	out, _ := lyra2rev2.Sum(input)
	reverseByByte(out)
	fmt.Println(hex.EncodeToString(out))
}
