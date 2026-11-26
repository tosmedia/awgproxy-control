package main

import "fmt"

// Строгий режим пока только для macOS.

func killSwitchSupported() bool { return false }
func killSwitchActive() bool    { return false }
func ksWanted() bool            { return false }
func ksInstalled() bool         { return false }
func ksRefresh()                {}
func setKillSwitch(on bool) error {
	return fmt.Errorf("строгий режим есть только на macOS")
}
func killSwitchDiag() string { return "" }
func bypassCheck() string    { return "Проверка доступна только на macOS\n" }
