package main

import (
	"log"
	"time"
)

type Issue struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

func flatten(err error) []string {
	if err == nil {
		return nil
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []string
		for _, e := range j.Unwrap() {
			out = append(out, flatten(e)...)
		}
		return out
	}
	return []string{err.Error()}
}

func (r *Runner) alert(issues []string, q *Quota, now time.Time) {
	var send [][2]string
	r.mu.Lock()
	if len(issues) > 0 {
		msg := issueSummary(issues)
		if shouldAlert(r.alertMsg, r.alertAt, now, msg) {
			r.alertMsg, r.alertAt = msg, now
			send = append(send, [2]string{"GoBridgeSync: backup problem", msg})
		}
		r.alerting = true
	} else if r.alerting {
		r.alerting, r.alertMsg = false, ""
		send = append(send, [2]string{"GoBridgeSync", "Backup is working again."})
	}
	if w := quotaWarning(q); w != "" && now.Sub(r.quotaAt) >= quotaAlertRepeat {
		r.quotaAt = now
		send = append(send, [2]string{"GoBridgeSync: storage almost full", w})
	}
	notify := r.notify
	r.mu.Unlock()
	for _, n := range send {
		if err := notify(n[0], n[1]); err != nil {
			log.Printf("notify: %v", err)
		}
	}
}

func mergeIssues(existing []Issue, msgs []string, now time.Time) []Issue {
	out := make([]Issue, 0, len(existing)+len(msgs))
	fresh := map[string]bool{}
	for _, m := range msgs {
		if !fresh[m] {
			fresh[m] = true
			out = append(out, Issue{Time: now, Message: m})
		}
	}
	for _, i := range existing {
		if !fresh[i.Message] {
			out = append(out, i)
		}
	}
	return out
}

func (r *Runner) ClearIssues() {
	r.set(func(s *Status) { s.Issues = nil })
	r.saveHistory()
}

func (r *Runner) recordIssues(msgs []string) {
	r.set(func(s *Status) {
		s.Issues = mergeIssues(s.Issues, msgs, time.Now())
		if len(s.Issues) > issueLimit {
			s.Issues = s.Issues[:issueLimit]
		}
	})
}
