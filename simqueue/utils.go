package simqueue

import (
	"encoding/binary"
	"errors"
	"time"
)

var errInvalidPackedData = errors.New("invalid packed data")

type packArgs struct {
	data           []byte
	minTargetBlock uint64
	maxTargetBlock uint64
	highPriority   bool
	timestamp      time.Time
	iteration      uint16
}

// packData returns score and packed data into a byte slice that can be stored in Redis.
// The score is the minTargetBlock.
// The format is (note that ':' is used only in the docs and not present in the actual data):
// highPriority(1byte):iteration(2 bytes):timestamp(8 bytes):maxblock(8 bytes):data
//
// This is done because redis sorts values with the same score by value lexicographically.
func packData(a packArgs) (float64, []byte) {
	score := float64(a.minTargetBlock)
	value := make([]byte, 19+len(a.data))
	if a.highPriority {
		value[0] = 0
	} else {
		value[0] = 1
	}
	binary.BigEndian.PutUint16(value[1:3], a.iteration)
	binary.BigEndian.PutUint64(value[3:11], uint64(a.timestamp.UnixNano()))
	binary.BigEndian.PutUint64(value[11:19], a.maxTargetBlock)
	copy(value[19:], a.data)
	return score, value
}

// unpackData unpacks the data from the byte slice returned by packData.
func unpackData(score float64, packedData []byte) (packArgs, error) {
	if len(packedData) < 19 {
		return packArgs{}, errInvalidPackedData
	}
	return packArgs{
		data:           packedData[19:],
		minTargetBlock: uint64(score),
		maxTargetBlock: binary.BigEndian.Uint64(packedData[11:19]),
		highPriority:   packedData[0] == 0,
		timestamp:      time.Unix(0, int64(binary.BigEndian.Uint64(packedData[3:11]))),
		iteration:      binary.BigEndian.Uint16(packedData[1:3]),
	}, nil
}
