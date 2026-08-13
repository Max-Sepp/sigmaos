package proc

import (
	"encoding/json"
	"fmt"
	"log"

	"sigmaos/serr"
)

type Tstatus uint8

const (
	StatusOK      Tstatus = 1
	StatusEvicted Tstatus = 2 // killed
	StatusErr     Tstatus = 3
	StatusFatal   Tstatus = 4 // to indicate to groupmgr that a proc shouldn't be restarted

	// for testing purposes, meaning sigma doesn't know what happened
	// to proc; machine might have crashed.
	CRASH       = 5
	CRASHSTATUS = "exit status 5"
)

func (status Tstatus) String() string {
	switch status {
	case StatusOK:
		return "OK"
	case StatusEvicted:
		return "EVICTED"
	case StatusErr:
		return "ERROR"
	case StatusFatal:
		return "FATAL"
	default:
		return "unkown status"
	}
}

type Status struct {
	StatusCode    Tstatus
	StatusMessage string
	StatusData    interface{}
}

func NewStatus(code Tstatus) *Status {
	return &Status{code, "", nil}
}

func NewStatusInfo(code Tstatus, msg string, data interface{}) *Status {
	return &Status{code, msg, data}
}

func NewStatusErr(msg string, data interface{}) *Status {
	return &Status{StatusErr, msg, data}
}

func NewStatusFromBytes(b []byte) *Status {
	status, err := StatusFromBytes(b)
	if err != nil {
		log.Fatalf("Error unmarshal status: %v", err)
	}
	return status
}

// StatusFromBytes is NewStatusFromBytes for a caller that can carry on without
// the status. Bytes that arrive over the network are the case that needs it:
// dying is a reasonable answer to a status this proc wrote and cannot read
// back, and no answer at all to one a peer sent.
//
// Empty is not an error. A proc may exit without a status, and there is
// nothing to report about that beyond the nil.
func StatusFromBytes(b []byte) (*Status, error) {
	if len(b) == 0 {
		return nil, nil
	}
	status := &Status{}
	if err := json.Unmarshal(b, status); err != nil {
		return nil, err
	}
	return status, nil
}

func (s *Status) IsStatusOK() bool {
	return s.StatusCode == StatusOK
}

func (s *Status) IsStatusEvicted() bool {
	return s.StatusCode == StatusEvicted
}

func (s *Status) IsStatusErr() bool {
	return s.StatusCode == StatusErr
}

func (s *Status) IsCrashed() bool {
	sr := serr.NewErrString(s.Msg())
	return sr.Code() == serr.TErrError && sr.Err.Error() == CRASHSTATUS
}

func (s *Status) IsStatusFatal() bool {
	return s.StatusCode == StatusFatal
}

func (s *Status) Msg() string {
	return s.StatusMessage
}

func (s *Status) Error() error {
	return fmt.Errorf("status error %s", s.StatusMessage)
}

func (s *Status) Data() interface{} {
	return s.StatusData
}

func (s *Status) String() string {
	return fmt.Sprintf("&{ statuscode:%v msg:%v data:%v }", s.StatusCode, s.StatusMessage, s.StatusData)
}

func (s *Status) Marshal() []byte {
	b, err := json.Marshal(s)
	if err != nil {
		log.Fatalf("Error marshal status: %v", err)
	}
	return b
}
