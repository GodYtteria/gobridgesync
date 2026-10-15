package main

type Action int

const (
	Noop Action = iota
	Download
	Upload
	DeleteLocal
	DeleteRemote
	Compare
	Forget
)

func (a Action) String() string {
	return [...]string{"noop", "download", "upload", "delete-local", "delete-remote", "compare", "forget"}[a]
}

func decide(b *Entry, l *LocalFile, r *RemoteFile) Action {
	if b == nil {
		switch {
		case l != nil && r != nil:
			return Compare
		case l != nil:
			return Upload
		case r != nil:
			return Download
		}
		return Noop
	}
	lChanged := l != nil && (l.Size != b.Size || l.MTime != b.MTime)
	rChanged := r != nil && (r.ID != b.ID || r.MD5 != b.MD5)
	switch {
	case l == nil && r == nil:
		return Forget
	case l == nil:
		if rChanged {
			return Download
		}
		return DeleteRemote
	case r == nil:
		if lChanged {
			return Upload
		}
		return DeleteLocal
	case lChanged && rChanged:
		return Compare
	case lChanged:
		return Upload
	case rChanged:
		return Download
	}
	return Noop
}

const massDeleteMax = 5000

func massDelete(deletes, known int) bool {
	return deletes > 20 && (deletes*2 > known || deletes > massDeleteMax)
}
