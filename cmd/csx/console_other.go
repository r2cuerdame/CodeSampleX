//go:build !windows

package main

func hideOwnConsoleWindow() {}

func maybeSyncLauncherDetached(string, string, []string) {}
