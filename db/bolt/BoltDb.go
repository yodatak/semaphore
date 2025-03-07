package bolt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/ansible-semaphore/semaphore/db"
	"github.com/ansible-semaphore/semaphore/util"
	"go.etcd.io/bbolt"
	"math/rand"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const MaxID = 2147483647

type enumerable interface {
	First() (key []byte, value []byte)
	Next() (key []byte, value []byte)
}

type emptyEnumerable struct{}

func (d emptyEnumerable) First() (key []byte, value []byte) {
	return nil, nil
}

func (d emptyEnumerable) Next() (key []byte, value []byte) {
	return nil, nil
}

type BoltDb struct {
	Filename string
	db       *bbolt.DB
}

type objectID interface {
	ToBytes() []byte
}

type intObjectID int
type strObjectID string

func (d intObjectID) ToBytes() []byte {
	return []byte(fmt.Sprintf("%010d", d))
}

func (d strObjectID) ToBytes() []byte {
	return []byte(d)
}

func makeBucketId(props db.ObjectProps, ids ...int) []byte {
	n := len(ids)

	id := props.TableName

	if !props.IsGlobal {
		for i := 0; i < n; i++ {
			id += fmt.Sprintf("_%010d", ids[i])
		}
	}

	return []byte(id)
}

func (d *BoltDb) Migrate() error {
	return nil
}

func (d *BoltDb) Connect() error {
	var filename string
	if d.Filename == "" {
		config, err := util.Config.GetDBConfig()
		if err != nil {
			return err
		}
		filename = config.Hostname
	} else {
		filename = d.Filename
	}

	var err error
	d.db, err = bbolt.Open(filename, 0666, &bbolt.Options{
		Timeout: 5 * time.Second,
	})

	if err != nil {
		return err
	}

	return nil
}

func (d *BoltDb) Close() error {
	return d.db.Close()
}

func (d *BoltDb) IsInitialized() (initialized bool, err error) {
	err = d.db.View(func(tx *bbolt.Tx) error {
		k, _ := tx.Cursor().First()
		initialized = k != nil
		return nil
	})
	return
}

func (d *BoltDb) getObject(bucketID int, props db.ObjectProps, objectID objectID, object interface{}) (err error) {
	err = d.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(makeBucketId(props, bucketID))
		if b == nil {
			return db.ErrNotFound
		}

		str := b.Get(objectID.ToBytes())
		if str == nil {
			return db.ErrNotFound
		}

		return unmarshalObject(str, object)
	})

	return
}

// getFieldNameByTagSuffix tries to find field by tag name and value in provided type.
// It returns error if field not found.
func getFieldNameByTagSuffix(t reflect.Type, tagName string, tagValueSuffix string) (string, error) {
	n := t.NumField()
	for i := 0; i < n; i++ {
		if strings.HasSuffix(t.Field(i).Tag.Get(tagName), tagValueSuffix) {
			return t.Field(i).Name, nil
		}
	}
	for i := 0; i < n; i++ {
		if t.Field(i).Tag != "" || t.Field(i).Type.Kind() != reflect.Struct {
			continue
		}
		str, err := getFieldNameByTagSuffix(t.Field(i).Type, tagName, tagValueSuffix)
		if err == nil {
			return str, nil
		}
	}
	return "", fmt.Errorf("field not found")
}

func sortObjects(objects interface{}, sortBy string, sortInverted bool) error {
	objectsValue := reflect.ValueOf(objects).Elem()
	objType := objectsValue.Type().Elem()

	fieldName, err := getFieldNameByTagSuffix(objType, "db", sortBy)
	if err != nil {
		return err
	}

	sort.SliceStable(objectsValue.Interface(), func(i, j int) bool {
		valueI := objectsValue.Index(i).FieldByName(fieldName)
		valueJ := objectsValue.Index(j).FieldByName(fieldName)

		less := false

		switch valueI.Kind() {
		case reflect.Int,
			reflect.Int8,
			reflect.Int16,
			reflect.Int32,
			reflect.Int64,
			reflect.Uint,
			reflect.Uint8,
			reflect.Uint16,
			reflect.Uint32,
			reflect.Uint64:
			less = valueI.Int() < valueJ.Int()
		case reflect.Float32:
		case reflect.Float64:
			less = valueI.Float() < valueJ.Float()
		case reflect.String:
			less = valueI.String() < valueJ.String()
		}

		if sortInverted {
			less = !less
		}

		return less
	})

	return nil
}

func createObjectType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	n := t.NumField()

	fields := make([]reflect.StructField, n)

	for i := 0; i < n; i++ {
		f := t.Field(i)
		tag := f.Tag.Get("db")
		if tag != "" {
			f.Tag = reflect.StructTag(`json:"` + tag + `"`)
		} else {
			if f.Type.Kind() == reflect.Struct {
				f.Type = createObjectType(f.Type)
			}
		}
		fields[i] = f
	}

	return reflect.StructOf(fields)
}

func unmarshalObject(data []byte, obj interface{}) error {
	newType := createObjectType(reflect.TypeOf(obj))
	ptr := reflect.New(newType).Interface()

	err := json.Unmarshal(data, ptr)
	if err != nil {
		return err
	}

	value := reflect.ValueOf(ptr).Elem()

	objValue := reflect.ValueOf(obj).Elem()

	for i := 0; i < newType.NumField(); i++ {
		objValue.Field(i).Set(value.Field(i))
	}

	return nil
}

func copyObject(obj interface{}, newType reflect.Type) interface{} {
	newValue := reflect.New(newType).Elem()

	oldValue := reflect.ValueOf(obj)

	for i := 0; i < newType.NumField(); i++ {
		var v interface{}
		if newValue.Field(i).Kind() == reflect.Struct &&
			newValue.Field(i).Type().PkgPath() == "" {
			v = copyObject(oldValue.Field(i).Interface(), newValue.Field(i).Type())
		} else {
			v = oldValue.Field(i).Interface()
		}
		newValue.Field(i).Set(reflect.ValueOf(v))
	}

	return newValue.Interface()
}

func marshalObject(obj interface{}) ([]byte, error) {
	newType := createObjectType(reflect.TypeOf(obj))
	return json.Marshal(copyObject(obj, newType))
}

func unmarshalObjects(rawData enumerable, props db.ObjectProps, params db.RetrieveQueryParams, filter func(interface{}) bool, objects interface{}) (err error) {
	objectsValue := reflect.ValueOf(objects).Elem()
	objType := objectsValue.Type().Elem()

	objectsValue.Set(reflect.MakeSlice(objectsValue.Type(), 0, 0))

	i := 0 // offset counter
	n := 0 // number of added items

	for k, v := rawData.First(); k != nil; k, v = rawData.Next() {
		if params.Offset > 0 && i < params.Offset {
			i++
			continue
		}

		tmp := reflect.New(objType)
		ptr := tmp.Interface()
		err = unmarshalObject(v, ptr)
		obj := reflect.ValueOf(ptr).Elem().Interface()

		if err != nil {
			return
		}

		if filter != nil {
			if !filter(obj) {
				continue
			}
		}

		newObjectValues := reflect.Append(objectsValue, reflect.ValueOf(obj))
		objectsValue.Set(newObjectValues)

		n++

		if params.Count > 0 && n > params.Count {
			break
		}
	}

	sortable := false

	if params.SortBy != "" {
		for _, v := range props.SortableColumns {
			if v == params.SortBy {
				sortable = true
				break
			}
		}
	}

	if sortable {
		err = sortObjects(objects, params.SortBy, params.SortInverted)
	}

	return
}

func (d *BoltDb) getObjects(bucketID int, props db.ObjectProps, params db.RetrieveQueryParams, filter func(interface{}) bool, objects interface{}) error {
	return d.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(makeBucketId(props, bucketID))
		var c enumerable
		if b == nil {
			c = emptyEnumerable{}
		} else {
			c = b.Cursor()
		}
		return unmarshalObjects(c, props, params, filter, objects)
	})
}

func (d *BoltDb) deleteObject(bucketID int, props db.ObjectProps, objectID objectID, tx *bbolt.Tx) error {
	for _, u := range []db.ObjectProps{db.TemplateProps, db.EnvironmentProps, db.InventoryProps, db.RepositoryProps} {
		inUse, err := d.isObjectInUse(bucketID, props, objectID, u)
		if err != nil {
			return err
		}
		if inUse {
			return db.ErrInvalidOperation
		}
	}

	fn := func(tx *bbolt.Tx) error {
		b := tx.Bucket(makeBucketId(props, bucketID))
		if b == nil {
			return db.ErrNotFound
		}
		return b.Delete(objectID.ToBytes())
	}

	if tx != nil {
		return fn(tx)
	}

	return d.db.Update(fn)
}

// updateObject updates data for object in database.
func (d *BoltDb) updateObject(bucketID int, props db.ObjectProps, object interface{}) error {
	return d.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(makeBucketId(props, bucketID))
		if b == nil {
			return db.ErrNotFound
		}

		idFieldName, err := getFieldNameByTagSuffix(reflect.TypeOf(object), "db", props.PrimaryColumnName)

		if err != nil {
			return err
		}

		idValue := reflect.ValueOf(object).FieldByName(idFieldName)

		var objID objectID

		switch idValue.Kind() {
		case reflect.Int,
			reflect.Int8,
			reflect.Int16,
			reflect.Int32,
			reflect.Int64,
			reflect.Uint,
			reflect.Uint8,
			reflect.Uint16,
			reflect.Uint32,
			reflect.Uint64:
			objID = intObjectID(idValue.Int())
		case reflect.String:
			objID = strObjectID(idValue.String())
		}

		if objID == nil {
			return fmt.Errorf("unsupported ID type")
		}

		if b.Get(objID.ToBytes()) == nil {
			return db.ErrNotFound
		}

		str, err := marshalObject(object)
		if err != nil {
			return err
		}

		return b.Put(objID.ToBytes(), str)
	})
}

func (d *BoltDb) createObject(bucketID int, props db.ObjectProps, object interface{}) (interface{}, error) {
	err := d.db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(makeBucketId(props, bucketID))

		if err != nil {
			return err
		}

		objPtr := reflect.ValueOf(&object).Elem()

		tmpObj := reflect.New(objPtr.Elem().Type()).Elem()
		tmpObj.Set(objPtr.Elem())

		var objID objectID

		if props.PrimaryColumnName != "" {
			idFieldName, err2 := getFieldNameByTagSuffix(reflect.TypeOf(object), "db", props.PrimaryColumnName)

			if err2 != nil {
				return err2
			}

			idValue := tmpObj.FieldByName(idFieldName)

			switch idValue.Kind() {
			case reflect.Int,
				reflect.Int8,
				reflect.Int16,
				reflect.Int32,
				reflect.Int64,
				reflect.Uint,
				reflect.Uint8,
				reflect.Uint16,
				reflect.Uint32,
				reflect.Uint64:
				if idValue.Int() == 0 {
					id, err3 := b.NextSequence()
					if err3 != nil {
						return err3
					}
					if props.SortInverted {
						id = MaxID - id
					}
					idValue.SetInt(int64(id))
				}

				objID = intObjectID(idValue.Int())
			case reflect.String:
				if idValue.String() == "" {
					return fmt.Errorf("object ID can not be empty string")
				}
				objID = strObjectID(idValue.String())
			case reflect.Invalid:
				id, err3 := b.NextSequence()
				if err3 != nil {
					return err3
				}
				objID = intObjectID(id)
			default:
				return fmt.Errorf("unsupported ID type")
			}
		} else {
			id, err2 := b.NextSequence()
			if err2 != nil {
				return err2
			}
			if props.SortInverted {
				id = MaxID - id
			}
			objID = intObjectID(id)
		}

		if objID == nil {
			return fmt.Errorf("object ID can not be nil")
		}

		objPtr.Set(tmpObj)
		str, err := marshalObject(object)
		if err != nil {
			return err
		}

		return b.Put(objID.ToBytes(), str)
	})

	return object, err
}

func (d *BoltDb) getObjectRefs(projectID int, objectProps db.ObjectProps, objectID int) (refs db.ObjectReferrers, err error) {
	refs.Templates, err = d.getObjectRefsFrom(projectID, objectProps, intObjectID(objectID), db.TemplateProps)
	if err != nil {
		return
	}

	refs.Repositories, err = d.getObjectRefsFrom(projectID, objectProps, intObjectID(objectID), db.RepositoryProps)
	if err != nil {
		return
	}

	refs.Inventories, err = d.getObjectRefsFrom(projectID, objectProps, intObjectID(objectID), db.InventoryProps)
	if err != nil {
		return
	}

	templates, err := d.getObjectRefsFrom(projectID, objectProps, intObjectID(objectID), db.ScheduleProps)

	for _, st := range templates {
		exists := false
		for _, tpl := range refs.Templates {
			if tpl.ID == st.ID {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		refs.Templates = append(refs.Templates, st)
	}

	return
}

func (d *BoltDb) getObjectRefsFrom(projectID int, objProps db.ObjectProps, objID objectID, referringObjectProps db.ObjectProps) (referringObjs []db.ObjectReferrer, err error) {
	referringObjs = make([]db.ObjectReferrer, 0)
	_, err = objProps.GetReferringFieldsFrom(referringObjectProps.Type)
	if err != nil {
		return
	}

	var referringObjects reflect.Value

	if referringObjectProps.Type == db.ScheduleProps.Type {
		schedules := make([]db.Schedule, 0)
		err = d.getObjects(projectID, db.ScheduleProps, db.RetrieveQueryParams{}, func(referringObj interface{}) bool {
			return isObjectReferredBy(objProps, objID, referringObj)
		}, &schedules)

		if err != nil {
			return
		}

		for _, schedule := range schedules {
			var template db.Template
			template, err = d.GetTemplate(projectID, schedule.TemplateID)
			if err != nil {
				return
			}
			referringObjs = append(referringObjs, db.ObjectReferrer{
				ID:   template.ID,
				Name: template.Name,
			})
		}
	} else {
		referringObjects = reflect.New(reflect.SliceOf(referringObjectProps.Type))
		err = d.getObjects(projectID, referringObjectProps, db.RetrieveQueryParams{}, func(referringObj interface{}) bool {
			return isObjectReferredBy(objProps, objID, referringObj)
		}, referringObjects.Interface())

		if err != nil {
			return
		}

		for i := 0; i < referringObjects.Elem().Len(); i++ {
			referringObjs = append(referringObjs, db.ObjectReferrer{
				ID:   int(referringObjects.Elem().Index(i).FieldByName("ID").Int()),
				Name: referringObjects.Elem().Index(i).FieldByName("Name").String(),
			})
		}
	}

	return
}

func isObjectReferredBy(props db.ObjectProps, objID objectID, referringObj interface{}) bool {
	if props.ReferringColumnSuffix == "" {
		return false
	}

	fieldName, err := getFieldNameByTagSuffix(reflect.TypeOf(referringObj), "db", props.ReferringColumnSuffix)

	if err != nil {
		return false
	}

	f := reflect.ValueOf(referringObj).FieldByName(fieldName)

	if f.IsZero() {
		return false
	}

	if f.Kind() == reflect.Ptr {
		if f.IsNil() {
			return false
		}

		f = f.Elem()
	}

	var fVal objectID
	switch f.Kind() {
	case reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64,
		reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64:
		fVal = intObjectID(f.Int())
	case reflect.String:
		fVal = strObjectID(f.String())
	}

	if fVal == nil {
		return false
	}

	return bytes.Equal(fVal.ToBytes(), objID.ToBytes())
}

// isObjectInUse checks if objID associated with any object in foreignTableProps.
func (d *BoltDb) isObjectInUse(bucketID int, objProps db.ObjectProps, objID objectID, referringObjectProps db.ObjectProps) (inUse bool, err error) {
	referringObjects := reflect.New(reflect.SliceOf(referringObjectProps.Type))

	err = d.getObjects(bucketID, referringObjectProps, db.RetrieveQueryParams{}, func(referringObj interface{}) bool {
		return isObjectReferredBy(objProps, objID, referringObj)
	}, referringObjects.Interface())

	if err != nil {
		return
	}

	inUse = referringObjects.Elem().Len() > 0

	return
}

func CreateTestStore() BoltDb {
	r := rand.New(rand.NewSource(time.Now().UTC().UnixNano()))
	fn := "/tmp/test_semaphore_db_" + strconv.Itoa(r.Int())
	store := BoltDb{
		Filename: fn,
	}
	err := store.Connect()
	if err != nil {
		panic(err)
	}
	return store
}
