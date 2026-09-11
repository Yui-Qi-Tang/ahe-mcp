//go:build !darwin

package main

import "errors"

func showStartupAlert(string) error {
	return errors.New("startup alerts require macOS")
}
