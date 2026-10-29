package access

import "fmt"

// Concurrency bounds a caller's complete requests, including vendor queues and
// streamed replies. Nil disables it. Zero QueueLimit rejects excess requests.
type Concurrency struct {
	MaxConcurrency int `json:"maxConcurrency"`
	QueueLimit     int `json:"queueLimit"`
	QueueWait      int `json:"queueWait"` // seconds; default 3 while queuing
}

func (c *Concurrency) Valid() (*Concurrency, error) {
	if c == nil {
		return nil, nil
	}
	if c.MaxConcurrency < 1 || c.MaxConcurrency > 100000 || c.QueueLimit < 0 || c.QueueLimit > 100000 || c.QueueWait < 0 || c.QueueWait > 300 {
		return nil, fmt.Errorf("caller concurrency must be 1..100000, queue 0..100000, wait 0..300 seconds")
	}
	v := *c
	if v.QueueLimit == 0 {
		v.QueueWait = 0
	} else if v.QueueWait == 0 {
		v.QueueWait = 3
	}
	return &v, nil
}
