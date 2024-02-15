package remoteimageasync

import (
	"context"
	"sync"
	"time"

	"github.com/warm-metal/container-image-csi-driver/pkg/remoteimage"
)

const prefix = "remoteimageasync"

type PullSession struct {
	image      string
	puller     remoteimage.Puller
	timeout    time.Duration    // this is the session timeout, not the caller timeout
	done       chan interface{} // chan will block until result
	isComplete bool
	isTimedOut bool
	err        error
}

type synchronizer struct {
	sessionMap      map[string]*PullSession
	mutex           *sync.Mutex
	sessions        chan *PullSession
	completedEvents chan string
	ctx             context.Context
}

// allows mocking/dependency injection
type AsyncPuller interface {
	// returns session that is ready to wait on, or error
	StartPull(image string, puller remoteimage.Puller, asyncPullTimeout time.Duration) (*PullSession, error)
	// waits for session to time out or succeed
	WaitForPull(session *PullSession, callerTimeout context.Context) error
}