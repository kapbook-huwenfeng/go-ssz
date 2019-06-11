package ssz

import (
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
)

// decodeError is what gets reported to the decoder user in error case.
type decodeError struct {
	msg string
	typ reflect.Type
}

func newDecodeError(msg string, typ reflect.Type) *decodeError {
	return &decodeError{msg, typ}
}

func (err *decodeError) Error() string {
	return fmt.Sprintf("decode error: %s for output type %v", err.msg, err.typ)
}

// Decode SSZ encoded data and output it into the object pointed by pointer val.
func Decode(input []byte, val interface{}) error {
	if val == nil {
		return newDecodeError("cannot decode into nil", nil)
	}
	rval := reflect.ValueOf(val)
	rtyp := rval.Type()
	// val must be a pointer, otherwise we refuse to decode
	if rtyp.Kind() != reflect.Ptr {
		return newDecodeError("can only decode into pointer target", rtyp)
	}
	if rval.IsNil() {
		return newDecodeError("cannot output to pointer of nil", rtyp)
	}
	sszUtils, err := cachedSSZUtils(rval.Elem().Type())
	if err != nil {
		return newDecodeError(fmt.Sprint(err), rval.Elem().Type())
	}
	if _, err = sszUtils.decoder(input, rval.Elem()); err != nil {
		return newDecodeError(fmt.Sprint(err), rval.Elem().Type())
	}
	return nil
}

func makeDecoder(typ reflect.Type) (dec decoder, err error) {
	kind := typ.Kind()
	switch {
	case kind == reflect.Bool:
		return decodeBool, nil
	case kind == reflect.Uint8:
		return decodeUint8, nil
	case kind == reflect.Uint16:
		return decodeUint16, nil
	case kind == reflect.Uint32:
		return decodeUint32, nil
	case kind == reflect.Int32:
		return decodeUint32, nil
	case kind == reflect.Uint64:
		return decodeUint64, nil
	case kind == reflect.Slice && typ.Elem().Kind() == reflect.Uint8:
		return decodeByteSlice, nil
	case kind == reflect.Array && typ.Elem().Kind() == reflect.Uint8:
		return decodeByteArray, nil
	case kind == reflect.Slice && isBasicType(typ.Elem().Kind()):
		return makeBasicSliceDecoder(typ)
	case kind == reflect.Slice && !isBasicType(typ.Elem().Kind()):
		return makeCompositeSliceDecoder(typ)
	case kind == reflect.Array:
		return makeArrayDecoder(typ)
	case kind == reflect.Struct:
		return makeStructDecoder(typ)
	case kind == reflect.Ptr:
		return makePtrDecoder(typ)
	default:
		return nil, fmt.Errorf("type %v is not deserializable", typ)
	}
}

func decodeBool(input []byte, val reflect.Value) (int, error) {
	v := uint8(input[0])
	if v == 0 {
		val.SetBool(false)
	} else if v == 1 {
		val.SetBool(true)
	} else {
		return 0, fmt.Errorf("expect 0 or 1 for decoding bool but got %d", v)
	}
	return 1, nil
}

func decodeUint8(input []byte, val reflect.Value) (int, error) {
	val.SetUint(uint64(input[0]))
	return 1, nil
}

func decodeUint16(input []byte, val reflect.Value) (int, error) {
	buf := make([]byte, 2)
	copy(buf, input)
	val.SetUint(uint64(binary.LittleEndian.Uint16(buf)))
	return 2, nil
}

func decodeUint32(input []byte, val reflect.Value) (int, error) {
	buf := make([]byte, 4)
	copy(buf, input)
	val.SetUint(uint64(binary.LittleEndian.Uint32(buf)))
	return 4, nil
}

func decodeUint64(input []byte, val reflect.Value) (int, error) {
	buf := make([]byte, 8)
	copy(buf, input)
	val.SetUint(binary.LittleEndian.Uint64(buf))
	return 8, nil
}

func decodeByteArray(input []byte, val reflect.Value) (int, error) {
	slice := val.Slice(0, val.Len()).Interface().([]byte)
	copy(slice, input)
	return len(input), nil
}

func decodeByteSlice(input []byte, val reflect.Value) (int, error) {
	val.SetBytes(input)
	return len(input), nil
}

func makeBasicSliceDecoder(typ reflect.Type) (decoder, error) {
	elemType := typ.Elem()
	elemSSZUtils, err := cachedSSZUtilsNoAcquireLock(elemType)
	if err != nil {
		return nil, err
	}
	decoder := func(input []byte, val reflect.Value) (int, error) {
		elemSize := basicElementSize(typ.Elem(), typ.Elem().Kind())
		size := len(input) / elemSize
		newVal := reflect.MakeSlice(val.Type(), size, size)
		reflect.Copy(newVal, val)
		val.Set(newVal)
		i, decodeSize := 0, uint64(0)
		elementIndex := 0
		for ; i < len(input); i += elemSize {
			elemDecodeSize, err := elemSSZUtils.decoder(input[i:i+elemSize], val.Index(elementIndex))
			if err != nil {
				return 0, fmt.Errorf("failed to decode element of slice: %v", err)
			}
			elementIndex++
			decodeSize += uint64(elemDecodeSize)
		}
		if decodeSize < uint64(size) {
			return 0, errors.New("input is too long")
		}
		return size, nil
	}
	return decoder, nil
}

func makeCompositeSliceDecoder(typ reflect.Type) (decoder, error) {
	elemType := typ.Elem()
	elemSSZUtils, err := cachedSSZUtilsNoAcquireLock(elemType)
	if err != nil {
		return nil, err
	}
	decoder := func(input []byte, val reflect.Value) (int, error) {
		elemSize := BytesPerLengthOffset
		size := len(input) / elemSize
		newVal := reflect.MakeSlice(val.Type(), size, size)
		reflect.Copy(newVal, val)
		val.Set(newVal)

		// Keep track of first offsets, current index, and iterate through the items.
		currentIndex := uint64(0)
		nextIndex := 0
		var firstOffset uint64
		if _, err := elemSSZUtils.decoder(input[:BytesPerLengthOffset], firstOffset); err != nil {
			return 0, err
		}
        currentOffset := firstOffset
        nextOffset := currentOffset
        for currentIndex < firstOffset {
        	if currentOffset > len(input) {
        		return 0, errors.New("offset out of bounds")
			}
		}

		return size, nil
	}
	return decoder, nil
}

func makeArrayDecoder(typ reflect.Type) (decoder, error) {
	elemType := typ.Elem()
	elemSSZUtils, err := cachedSSZUtilsNoAcquireLock(elemType)
	if err != nil {
		return nil, err
	}
	decoder := func(input []byte, val reflect.Value) (int, error) {
		size := val.Len()
		i, decodeSize := 0, 0
        offsetIndex := 0
		elemSize := basicElementSize(typ, typ.Elem().Kind())
		for ; i < size; i++ {
			elemDecodeSize, err := elemSSZUtils.decoder(input[offsetIndex:offsetIndex+elemSize], val.Index(i))
			if err != nil {
				return 0, fmt.Errorf("failed to decode element of slice: %v", err)
			}
			decodeSize += elemDecodeSize
			offsetIndex += elemSize
		}
		if decodeSize < size {
			return 0, errors.New("input is too long")
		}
		return decodeSize, nil
	}
	return decoder, nil
}

func makeStructDecoder(typ reflect.Type) (decoder, error) {
	fields, err := structFields(typ)
	if err != nil {
		return nil, err
	}
	decoder := func(input []byte, val reflect.Value) (int, error) {
		size := len(input)

		if size == 0 {
			return 0, nil
		}

		i := 0
		offsetIndex := 0
		for ; i < len(fields); i++ {
			// Track the offset index verifying if a field is variable-size or fixed-size, and then proceed.
			f := fields[i]
			// TODO: Handle is variadic.
			elemSize := basicElementSize(val.Field(f.index).Type(), val.Field(f.index).Kind())
			fieldDecodeSize, err := f.sszUtils.decoder(input[offsetIndex:offsetIndex+elemSize], val.Field(f.index))
			if err != nil {
				return 0, fmt.Errorf("failed to decode field of slice: %v", err)
			}
			offsetIndex += fieldDecodeSize
		}
		if i < len(fields) {
			return 0, errors.New("input is too short")
		}
		return offsetIndex, nil
	}
	return decoder, nil
}

func makePtrDecoder(typ reflect.Type) (decoder, error) {
	elemType := typ.Elem()
	elemSSZUtils, err := cachedSSZUtilsNoAcquireLock(elemType)
	if err != nil {
		return nil, err
	}
	decoder := func(input []byte, val reflect.Value) (int, error) {
		newVal := reflect.New(elemType)
		elemDecodeSize, err := elemSSZUtils.decoder(input, newVal.Elem())
		if err != nil {
			return 0, fmt.Errorf("failed to decode to object pointed by pointer: %v", err)
		}
		return elemDecodeSize, nil
	}
	return decoder, nil
}
