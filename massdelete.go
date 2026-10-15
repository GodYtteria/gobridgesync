package main

import (
	"fmt"
)

const massDeleteWaiting = "waiting for you to allow it"

type massDeleteError struct{ deletes, known int }

func (e *massDeleteError) Error() string {
	return fmt.Sprintf("%d of %d files would be deleted in one go; %s", e.deletes, e.known, massDeleteWaiting)
}
