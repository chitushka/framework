package main

import "github.com/chitushka/framework/internal/starter"

func newBoundedSessions(limit int) *starter.SessionBackend { return starter.NewSessionBackend(limit) }
