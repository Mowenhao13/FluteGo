package filedesc

import (
	"os"
	utils "FluteGo/pkg/utils"
)

type FileDesc struct {
	FdtID       uint8
	SendPath    string
	SaveDir    string
	Name        string
	TransferLen uint64
	ContentType string
	Md5         string
}

func GetFileDesc(file *os.File, fdtID uint8, saveDir string) (*FileDesc, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if info.Size() == 0 {
		return nil, os.ErrInvalid
	}

	md5, err := utils.CalculateMd5(file)
	if err != nil {
		return nil, err
	}
	
	fd := &FileDesc{
		FdtID:       fdtID,
		SendPath:    file.Name(),
		SaveDir:     saveDir,
		Name:        info.Name(),
		TransferLen: uint64(info.Size()),
		ContentType: utils.GetContentType(file),
		Md5:         md5,
	}

	return fd, nil
}
