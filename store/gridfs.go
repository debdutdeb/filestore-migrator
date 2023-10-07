package store

import (
	"errors"

	"github.com/RocketChat/filestore-migrator/rocketchat"
	"go.mongodb.org/mongo-driver/mongo"
)

// GridFSProvider provides methods to use GridFS as a storage provider.
type GridFSProvider struct {
	Database         string
	Session          mongo.Session
	TempFileLocation string
}

// StoreType returns the name of the store
func (g *GridFSProvider) StoreType() string {
	return "GridFS"
}

// SetTempDirectory allows for the setting of the directory that will be used for temporary file store during operations
func (g *GridFSProvider) SetTempDirectory(dir string) {
	g.TempFileLocation = dir
}

// Download downloads a file from the storage provider and moves it to the temporary file store
func (g *GridFSProvider) Download(fileCollection string, file rocketchat.File) (string, error) {
	// FIXME implement gridfs download
	/* 	gridfs.
	   	gridFile, err := sess.DB(g.Database).GridFS(fileCollection).Open(file.ID)
	   	if err != nil {
	   		if err == mgo.ErrNotFound {
	   			return "", ErrNotFound
	   		}

	   		return "", err
	   	}

	   	defer gridFile.Close()

	   	filePath := g.TempFileLocation + "/" + file.ID

	   	if _, err := os.Stat(filePath); os.IsNotExist(err) {

	   		f, err := os.Create(filePath)
	   		if err != nil {
	   			return "", err
	   		}

	   		defer f.Close()

	   		if _, err = io.Copy(f, gridFile); err != nil {
	   			return "", err
	   		}
	   	}

	   	return filePath, err */
	return "", nil
}

// Upload uploads a file from given path to the storage provider (not implemented)
func (g *GridFSProvider) Upload(path string, filePath string, contentType string) error {
	return nil
}

func (s *GridFSProvider) Delete(file rocketchat.File, permanentelyDelete bool) error {
	return errors.New("delete object method not implemented")
}
