package container

import (
	"sigmaos/proc"
)

type ProcContainer interface {
	Pid() int
	GetPSS() (proc.Tmem, error)
	Wait() error
}
