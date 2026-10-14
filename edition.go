package main

import "encoding/base64"

var (
	edition        = "external"
	embeddedClient = ""
	updateBase     = ""
)

const syncRootName = "GoBridgeSync"

func internalEdition() bool { return edition == "internal" }

func syncPrefix() string {
	if internalEdition() {
		return ""
	}
	return syncRootName
}

func syncFolderName() string {
	if internalEdition() {
		return "GoogleDrive"
	}
	return syncRootName
}

func builtInClient() ([]byte, bool) {
	if embeddedClient == "" {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(embeddedClient)
	return b, err == nil
}

func hasClient(p Paths) bool {
	if _, ok := builtInClient(); ok {
		return true
	}
	return fileExists(p.credentials())
}

func appID() string {
	if internalEdition() {
		return "gobridgesync-bukluran"
	}
	return "gobridgesync"
}

func defaultAddr() string {
	if internalEdition() {
		return "127.0.0.1:47812"
	}
	return "127.0.0.1:47811"
}
