package test

import (
	"FluteGo/constant"
	"FluteGo/pkg/utils"
	"testing"
)

func TestListDir(t *testing.T) {
	fPath := constant.SendFileDir_win_t
	utils.ListDir(fPath)
}
