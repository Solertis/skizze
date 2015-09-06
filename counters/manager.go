package counters

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/seiflotfy/skizze/config"
	"github.com/seiflotfy/skizze/counters/abstract"
	"github.com/seiflotfy/skizze/counters/wrappers/count-min-log"
	"github.com/seiflotfy/skizze/counters/wrappers/hllpp"
	"github.com/seiflotfy/skizze/counters/wrappers/topk"
	"github.com/seiflotfy/skizze/storage"
	"github.com/seiflotfy/skizze/utils"
)

/*
ManagerStruct is responsible for manipulating the counters and syncing to disk
*/
type ManagerStruct struct {
	sketches map[string]abstract.Counter
	info     map[string]*abstract.Info
}

var manager *ManagerStruct
var logger = utils.GetLogger()

/*
CreateSketch ...
*/
func (m *ManagerStruct) CreateSketch(sketchID string, sketchType string, props map[string]float64) error {
	id := fmt.Sprintf("%s.%s", sketchID, sketchType)

	// Check if sketch with ID already exists
	if info, ok := m.info[id]; ok {
		errStr := fmt.Sprintf("Sketch %s of type %s already exists", id, info.Type)
		return errors.New(errStr)
	}

	if len([]byte(id)) > config.MaxKeySize {
		errStr := fmt.Sprintf("Invalid length of sketch ID: %d. Max length allowed: %d", len(id), config.MaxKeySize)
		return errors.New(errStr)
	}
	if sketchType == "" {
		logger.Error.Println("SketchType is mandatory and must be set!")
		return errors.New("No sketch type was given!")
	}
	info := &abstract.Info{ID: id,
		Type:       sketchType,
		Properties: props,
		State:      make(map[string]uint64)}
	var sketch abstract.Counter
	var err error
	switch sketchType {
	case abstract.HLLPP:
		sketch, err = hllpp.NewSketch(info)
	case abstract.TopK:
		sketch, err = topk.NewSketch(info)
	case abstract.CML:
		sketch, err = cml.NewSketch(info)
	default:
		return errors.New("Invalid sketch type: " + sketchType)
	}

	if err != nil {
		errTxt := fmt.Sprint("Could not load sketch ", info, ". Err:", err)
		return errors.New(errTxt)
	}
	m.sketches[info.ID] = sketch
	m.dumpInfo(info)
	return nil
}

/*
DeleteSketch ...
*/
func (m *ManagerStruct) DeleteSketch(sketchID string, sketchType string) error {
	id := fmt.Sprintf("%s.%s", sketchID, sketchType)

	if _, ok := m.sketches[id]; !ok {
		return errors.New("No such sketch " + sketchID)
	}
	delete(m.sketches, id)
	delete(m.info, id)
	manager := storage.GetManager()
	err := manager.DeleteInfo(id)
	if err != nil {
		return err
	}
	return manager.DeleteData(id)
}

/*
GetSketches ...
*/
func (m *ManagerStruct) GetSketches() ([]string, error) {
	// TODO: Remove dummy data and implement proper result
	sketches := make([]string, len(m.sketches), len(m.sketches))
	i := 0
	for _, v := range m.sketches {
		typ := v.GetType()
		id := v.GetID()
		sketches[i] = fmt.Sprintf("%s/%s", typ, id[:len(id)-len(typ)-1])
		i++
	}
	return sketches, nil
}

/*
AddToSketch ...
*/
func (m *ManagerStruct) AddToSketch(sketchID string, sketchType string, values []string) error {
	id := fmt.Sprintf("%s.%s", sketchID, sketchType)

	var val, ok = m.sketches[id]
	if ok == false {
		errStr := fmt.Sprintf("No such sketch %s of type %s found", sketchID, sketchType)
		return errors.New(errStr)
	}
	var counter abstract.Counter
	counter = val.(abstract.Counter)

	bytes := make([][]byte, len(values), len(values))
	for i, value := range values {
		bytes[i] = []byte(value)
	}
	counter.AddMultiple(bytes)
	return nil
}

/*
DeleteFromSketch ...
*/
func (m *ManagerStruct) DeleteFromSketch(sketchID string, sketchType string, values []string) error {
	var val, ok = m.sketches[sketchID]
	if ok == false {
		return errors.New("No such sketch: " + sketchID)
	}
	var counter abstract.Counter
	counter = val.(abstract.Counter)

	bytes := make([][]byte, len(values), len(values))
	for i, value := range values {
		bytes[i] = []byte(value)
	}
	ok, err := counter.RemoveMultiple(bytes)
	return err
}

/*
GetCountForSketch ...
*/
func (m *ManagerStruct) GetCountForSketch(sketchID string, sketchType string, values []string) (interface{}, error) {
	id := fmt.Sprintf("%s.%s", sketchID, sketchType)
	var val, ok = m.sketches[id]
	if ok == false {
		errStr := fmt.Sprintf("No such sketch %s of type %s found", sketchID, sketchType)
		return 0, errors.New(errStr)
	}
	var counter abstract.Counter
	counter = val.(abstract.Counter)

	if counter.GetType() == abstract.CML {
		bvalues := make([][]byte, len(values), len(values))
		for i, value := range values {
			bvalues[i] = []byte(value)
		}
		return counter.GetFrequency(bvalues), nil
	} else if counter.GetType() == abstract.TopK {
		return counter.GetFrequency(nil), nil
	}

	count := counter.GetCount()
	return count, nil
}

/*
GetManager returns a singleton Manager
*/
func GetManager() (*ManagerStruct, error) {
	var err error
	if manager == nil {
		manager, err = newManager()
	}
	if err != nil {
		return nil, err
	}
	return manager, nil
}

func newManager() (*ManagerStruct, error) {
	sketches := make(map[string]abstract.Counter)
	m := &ManagerStruct{sketches, make(map[string]*abstract.Info)}
	err := m.loadInfo()
	if err != nil {
		return nil, err
	}
	err = m.loadSketches()
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (m *ManagerStruct) dumpInfo(i *abstract.Info) {
	m.info[i.ID] = i
	manager := storage.GetManager()
	infoData, err := json.Marshal(i)
	utils.PanicOnError(err)
	manager.SaveInfo(i.ID, infoData)
}

func (m *ManagerStruct) loadInfo() error {
	manager := storage.GetManager()
	var infoStruct abstract.Info
	infos, err := manager.LoadAllInfo()
	if err != nil {
		return err
	}
	for _, infoData := range infos {
		json.Unmarshal(infoData, &infoStruct)
		m.info[infoStruct.ID] = &infoStruct
	}
	return nil
}

func (m *ManagerStruct) loadSketches() error {
	strg := storage.GetManager()
	for key, info := range m.info {
		var sketch abstract.Counter
		var err error
		switch info.Type {
		case abstract.HLLPP:
			sketch, err = hllpp.NewSketchFromData(info)
		case abstract.TopK:
			sketch, err = topk.NewSketchFromData(info)
		case abstract.CML:
			sketch, err = cml.NewSketchFromData(info)
		default:
			logger.Info.Println("Invalid counter type", info.Type)
		}
		if err != nil {
			errTxt := fmt.Sprint("Could not load sketch ", info, ". Err: ", err)
			return errors.New(errTxt)
		}
		m.sketches[info.ID] = sketch
		strg.LoadData(key, 0, 0)
	}
	return nil
}

/*
Destroy ...
*/
func (m *ManagerStruct) Destroy() {
	manager = nil
}
