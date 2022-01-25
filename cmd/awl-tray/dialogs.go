package main

import (
	"github.com/ncruces/zenity"
)

func handleErrorWithDialog(err error) {
	if err == nil {
		return
	}
	logger.Error(err)
	dialogErr := zenity.Error(err.Error(), zenity.Title("Anywherelan error"), zenity.ErrorIcon)
	if dialogErr != nil {
		logger.Errorf("show dialog: error handling: %v", dialogErr)
	}
}

func showInfoDialog(title, message string) {
	err := zenity.Info(message, zenity.Title(title), zenity.InfoIcon, zenity.Width(250))
	if err != nil {
		logger.Errorf("show dialog: info: %v", err)
	}
}

func showQuestionDialog(title, message, okLabel string) bool {
	err := zenity.Question(message, zenity.Title(title), zenity.QuestionIcon, zenity.OKLabel(okLabel), zenity.Width(250))
	switch {
	case err == zenity.ErrCanceled:
		return false
	case err != nil:
		logger.Errorf("show dialog: question: %v", err)
	}
	return true
}
