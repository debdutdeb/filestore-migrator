package migrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RocketChat/filestore-migrator/rocketchat"
	"github.com/RocketChat/filestore-migrator/store"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type rocketChatSetting struct {
	ID    string `bson:"_id"`
	Value string
}

// DebugMode sets debug mode on
func (m *Migrate) DebugMode() {
	m.debug = true
}

func (m *Migrate) debugLog(v ...interface{}) {
	if m.debug {
		logger(v...)
	}
}

func logger(v ...interface{}) {
	all := append([]interface{}{fmt.Sprintf("[%s]", time.Now().Format("01/02/2006 15:04:05"))}, v...)
	log.Println(all...)
}

// SetFileDelay set the delay between
func (m *Migrate) SetFileDelay(duration time.Duration) {
	m.fileDelay = duration
}

// SetStoreName that will be operating on
func (m *Migrate) SetStoreName(storeName string) error {
	if storeName != "Uploads" && storeName != "Avatars" {
		return errors.New("Invalid Store Name")
	}

	m.storeName = storeName

	if m.sourceStore != nil {
		m.sourceStore.SetTempDirectory(m.tempFileLocation + "/" + strings.ToLower(storeName))
	}

	if m.destinationStore != nil {
		m.destinationStore.SetTempDirectory(m.tempFileLocation + "/" + strings.ToLower(storeName))
	}

	return nil
}

func (m *Migrate) getFiles() ([]rocketchat.File, error) {
	if m.storeName == "" {
		return nil, errors.New("no store Name")
	}

	m.debugLog("Store: ", m.storeName)

	fileCollection := ""

	switch m.storeName {
	case "Uploads":
		fileCollection = "rocketchat_uploads"
	case "Avatars":
		fileCollection = "rocketchat_avatars"
	default:
		return nil, errors.New("Invalid store Name")
	}

	m.fileCollectionName = fileCollection

	session, err := connectDB(m.connectionString)
	if err != nil {
		return nil, err
	}

	m.session = session

	db := session.Client().Database(m.databaseName)

	settingsCollection := db.Collection("rocketchat_settings")

	var uniqueID rocketChatSetting

	if err := settingsCollection.FindOne(context.TODO(), bson.M{"_id": "uniqueID"}).Decode(&uniqueID); err != nil {
		return nil, err
	}

	m.debugLog("uniqueId", uniqueID)
	m.uniqueID = uniqueID.Value

	collection := db.Collection(fileCollection)

	var files []rocketchat.File

	m.debugLog(fileCollection, m.sourceStore.StoreType()+":"+m.storeName)

	query := bson.M{"store": m.sourceStore.StoreType() + ":" + m.storeName}

	if !m.fileOffset.IsZero() {
		query["uploadedAt"] = bson.M{"$gte": m.fileOffset}
	}

	if cursor, err := collection.Find(context.TODO(), query); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, errors.New("No files found")
		}

		return nil, err
	} else {
		if err = cursor.All(context.TODO(), &files); err != nil {
			return nil, err
		}
	}

	return files, nil
}

// MigrateStore migrates a filestore between source and destination
func (m *Migrate) MigrateStore() error {
	if m.sourceStore == nil || m.destinationStore == nil {
		return errors.New("For MigrateStore both a source and destionation store must be provided")
	}

	files, err := m.getFiles()
	if err != nil {
		return err
	}

	m.debugLog(fmt.Sprintf("Found %v files\n", len(files)))

	var maxConcurrency int = 1
	v, ok := os.LookupEnv("MAX_CONCURRENCY")
	if ok {
		maxConcurrency, err = strconv.Atoi(v)
		if err != nil {
			return err
		}
	}

	errChan := make(chan error)
	oneDone := make(chan bool)

	go func(c chan error) {
		e := <-c
		if e == nil {
			return
		}
		panic(e)
	}(errChan)

	for i, file := range files {
		if i == maxConcurrency {
			// wait before scheduling again
			<-oneDone
		}

		index := i + 1 // for logs

		file := file

		go func(doneChan chan bool, errChan chan error) {
			m.debugLog(fmt.Sprintf("[%v/%v] Downloading %s from: %s\n", index, len(files), file.Name, m.sourceStore.StoreType()))

			if !file.Complete {
				m.debugLog(fmt.Sprintf("[%v/%v] File wasn't completed uploading for %s Skipping\n", index, len(files), file.Name))
				doneChan <- true
				return
			}

			downloadedPath, err := m.sourceStore.Download(m.fileCollectionName, file)
			if err != nil {
				if err == store.ErrNotFound || m.skipErrors {
					m.debugLog(fmt.Sprintf("[%v/%v] No corresponding file for %s Skipping\n", index, len(files), file.Name))
					doneChan <- true
					return
				} else {
					errChan <- err
					return
				}
			}

			if file.Rid == "" && m.storeName == "Uploads" {
				file.Rid = "undefined"
			}

			if file.UserID == "" {
				file.UserID = "undefined"
			}

			objectPath := m.getObjectPath(&file)

			m.debugLog(fmt.Sprintf("[%v/%v] Uploading to %s to: %s\n", index, len(files), m.destinationStore.StoreType(), objectPath))

			if err := m.destinationStore.Upload(objectPath, downloadedPath, file.Type); err != nil {
				errChan <- err
				return
			}

			unset := m.fixFileForUpload(&file, objectPath)

			update := bson.M{
				"$set": file,
			}

			if unset != "" {
				update["$unset"] = bson.M{unset: 1}
			}

			db := m.session.Client().Database(m.databaseName)
			collection := db.Collection(m.fileCollectionName)

			if _, err := collection.UpdateOne(context.TODO(), bson.M{"_id": file.ID}, update); err != nil {
				errChan <- err
				return
			}

			m.debugLog(fmt.Sprintf("[%v/%v] Completed Uploading %s\n", index, len(files), file.Name))

			time.Sleep(m.fileDelay)

			doneChan <- true

		}(oneDone, errChan)
	}

	<-oneDone

	errChan <- nil

	m.debugLog("Finished!")

	return nil
}

func (m *Migrate) getObjectPath(file *rocketchat.File) string {
	objectPath := ""

	switch m.storeName {
	case "Uploads":
		objectPath = fmt.Sprintf("%s/%s/%s/%s/%s", m.uniqueID, strings.ToLower(m.storeName), file.Rid, file.UserID, file.ID)
	case "Avatars":
		objectPath = fmt.Sprintf("%s/%s/%s", m.uniqueID, strings.ToLower(m.storeName), file.UserID)
	}

	// FileSystem just dumps them in the folder based on the ID
	if m.destinationStore.StoreType() == "FileSystem" {
		objectPath = file.ID
	}

	return objectPath
}

func (m *Migrate) fixFileForUpload(file *rocketchat.File, objectPath string) string {
	unset := ""

	switch m.destinationStore.StoreType() {
	case "AmazonS3":
		file.AmazonS3 = rocketchat.AmazonS3{
			Path: objectPath,
		}

		// Set to empty object so won't be saved back
		unset = "GoogleStorage"
		file.GoogleStorage = rocketchat.GoogleStorage{}

	case "GoogleCloudStorage":
		file.GoogleStorage = rocketchat.GoogleStorage{
			Path: objectPath, // Set to empty object so won't be saved back
		}

		// Set to empty object so won't be saved back
		unset = "AmazonS3"
		file.AmazonS3 = rocketchat.AmazonS3{}
	case "FileSystem":
	default:
	}

	ufsPath := fmt.Sprintf("/ufs/%s:%s/%s/%s", m.destinationStore.StoreType(), m.storeName, file.ID, file.Name)

	file.URL = ufsPath
	file.Path = ufsPath
	file.Store = m.destinationStore.StoreType() + ":" + m.storeName

	return unset
}

// SetFileOffset sets an offset for file upload/downloads
func (m *Migrate) SetFileOffset(offset time.Time) error {
	if offset.IsZero() {
		return errors.New("invalid date")
	}

	m.fileOffset = offset

	return nil
}

// DownloadAll downloads all files from a filestore
func (m *Migrate) DownloadAll() error {
	if m.sourceStore == nil {
		return errors.New("For DownloadAll must have a source store provided")
	}

	files, err := m.getFiles()
	if err != nil {
		return err
	}

	m.debugLog(fmt.Sprintf("Found %v files\n", len(files)))

	maxRun := 1
	if v, ok := os.LookupEnv("MAX_CONCURRENCY"); ok {
		fmt.Printf("MAX_CONCURRENCY: %s\n", v)
		time.Sleep(time.Second * 10)
		maxRun, err = strconv.Atoi(v)
		if err != nil {
			return err
		}
	}

	doneChan := make(chan bool)
	errChan := make(chan error)

	go func() {
		e := <-errChan
		if e == nil {
			return
		}
		panic(e)
	}()

	var wg sync.WaitGroup

	for i, file := range files {
		if i == maxRun {
			<-doneChan
		}

		index := i + 1 // for logs

		file := file

		wg.Add(1)
		go func() {

			defer wg.Done()

			m.debugLog(fmt.Sprintf("[%v/%v] Downloading %s from: %s\n", index, len(files), file.Name, m.sourceStore.StoreType()))

			if !file.Complete {
				fmt.Printf("[%v/%v] rocketchat.File wasn't completed uploading for %s Skipping\n", index, len(files), file.Name)
				doneChan <- true
				return
			}

			if _, err := m.sourceStore.Download(m.fileCollectionName, file); err != nil {
				if errors.Is(err, store.ErrNotFound) || m.skipErrors {
					fmt.Printf("[%v/%v] No corresponding file for %s Skipping\n", index, len(files), file.Name)
					doneChan <- true
					return
				} else {
					errChan <- err
					return
				}
			}

			m.debugLog(fmt.Sprintf("[%v/%v] Downloaded %s from: %s\n", index, len(files), file.Name, m.sourceStore.StoreType()))
			doneChan <- true
		}()

		time.Sleep(m.fileDelay)
	}

	wg.Wait()

	m.debugLog("Finished!")

	return nil
}

// UploadAll uploads all files from a filestore
func (m *Migrate) UploadAll(filesRoot string) error {
	if m.destinationStore == nil {
		return errors.New("For UploadAll must have a destination store provided")
	}

	files, err := m.getFiles()
	if err != nil {
		return err
	}

	m.debugLog(fmt.Sprintf("Found %v files in database\n", len(files)))

	filesRoot = filesRoot + "/" + strings.ToLower(m.storeName)

	for i, file := range files {
		index := i + 1 // for logs

		fileLocation := filesRoot + "/" + file.ID

		if _, err := os.Stat(fileLocation); os.IsNotExist(err) {
			log.Println("Failed to locate: ", file.Name)
			continue
		}

		m.debugLog(fmt.Sprintf("[%v/%v] Uploading %s to: %s\n", index, len(files), file.Name, m.destinationStore.StoreType()))

		if !file.Complete {
			fmt.Printf("[%v/%v] rocketchat.File wasn't completed uploading for %s Skipping\n", index, len(files), file.Name)
			continue
		}

		objectPath := m.getObjectPath(&file)

		m.debugLog(fmt.Sprintf("[%v/%v] Uploading to %s to: %s\n", index, len(files), m.destinationStore.StoreType(), objectPath))
		if err := m.destinationStore.Upload(objectPath, fileLocation, file.Type); err != nil {
			return err
		}

		unset := m.fixFileForUpload(&file, objectPath)

		update := bson.M{
			"$set": file,
		}

		if unset != "" {
			update["$unset"] = bson.M{unset: 1}
		}

		collection := m.session.Client().Database(m.databaseName).Collection(m.fileCollectionName)

		if _, err := collection.UpdateOne(context.TODO(), bson.M{"_id": file.ID}, update); err != nil {
			return err
		}

		m.debugLog(fmt.Sprintf("[%v/%v] Completed Uploading %s\n", index, len(files), file.Name))

		time.Sleep(m.fileDelay)
	}

	m.debugLog("Finished!")

	return nil
}
