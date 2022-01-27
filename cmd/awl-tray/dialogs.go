package main

import (
	"os/exec"

	"github.com/ncruces/zenity"
)

var kdialogAvailable bool

func init() {
	_, err := exec.LookPath("kdialog")
	kdialogAvailable = err == nil
}

func handleErrorWithDialog(err error) {
	if err == nil {
		return
	}
	logger.Error(err)
	showErrorDialog("Anywherelan error", err.Error())
}

func showErrorDialog(title, message string) {
	var err error
	if kdialogAvailable {
		args := []string{"--error", message, "--title", title, "--icon", "dialog-error"}
		_, err = exec.Command("kdialog", args...).Output()
	} else {
		err = zenity.Error(message, zenity.Title(title), zenity.ErrorIcon)
	}
	if err != nil {
		logger.Errorf("show dialog: error handling: %v", err)
	}
}

func showInfoDialog(title, message string) {
	var err error
	if kdialogAvailable {
		args := []string{"--msgbox", message, "--title", title, "--icon", "dialog-information"}
		_, err = exec.Command("kdialog", args...).Output()
	} else {
		err = zenity.Info(message, zenity.Title(title), zenity.InfoIcon, zenity.Width(250))
	}
	if err != nil {
		logger.Errorf("show dialog: info: %v", err)
	}
}

func showQuestionDialog(title, message, okLabel string) bool {
	var err error
	if kdialogAvailable {
		args := []string{"--yesno", message, "--title", title, "--icon", "dialog-question", "--yes-label", okLabel}
		_, err = exec.Command("kdialog", args...).Output()
		if execErr, ok := err.(*exec.ExitError); ok && execErr.ExitCode() == 1 {
			err = zenity.ErrCanceled
		}
	} else {
		err = zenity.Question(message, zenity.Title(title), zenity.QuestionIcon, zenity.OKLabel(okLabel), zenity.Width(250))
	}
	switch {
	case err == zenity.ErrCanceled:
		return false
	case err != nil:
		logger.Errorf("show dialog: question: %v", err)
	}
	return true
}
