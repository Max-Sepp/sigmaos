package container

type ProcContainer interface {
	Pid() int
	Wait() error
}
