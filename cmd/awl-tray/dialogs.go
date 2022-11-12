package main

import (
	"github.com/ncruces/zenity"
)

func handleErrorWithDialog(err error) {
	if err == nil {
		return
	}
	logger.Error(err)
	showErrorDialog("Anywherelan error", err.Error())
}

func showErrorDialog(title, message string) {
	uid, hasUID := getRealUserID()
	opts := []zenity.Option{zenity.Title(title), zenity.ErrorIcon}
	if hasUID {
		opts = append(opts, zenity.UnixUID(uid))
	}
	err := zenity.Error(message, opts...)
	if err != nil {
		logger.Errorf("show dialog: error handling: %v", err)
	}
}

func showInfoDialog(title, message string) {
	uid, hasUID := getRealUserID()
	opts := []zenity.Option{zenity.Title(title), zenity.InfoIcon, zenity.Width(250)}
	if hasUID {
		opts = append(opts, zenity.UnixUID(uid))
	}
	err := zenity.Info(message, opts...)
	if err != nil {
		logger.Errorf("show dialog: info: %v", err)
	}
}

func showQuestionDialog(title, message, okLabel string) bool {
	uid, hasUID := getRealUserID()
	opts := []zenity.Option{zenity.Title(title), zenity.QuestionIcon, zenity.OKLabel(okLabel), zenity.Width(250)}
	if hasUID {
		opts = append(opts, zenity.UnixUID(uid))
	}
	err := zenity.Question(message, opts...)
	switch {
	case err == zenity.ErrCanceled:
		return false
	case err != nil:
		logger.Errorf("show dialog: question: %v", err)
	}
	return true
}
