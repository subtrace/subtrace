package tunnel

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
	"subtrace.dev/event"
)

var EventFields []*EventField

func initEventFields() error {
	desc := ((*event.Event)(nil)).ProtoReflect().Descriptor()
	for i := 0; i < desc.Fields().Len(); i++ {
		field := desc.Fields().Get(i)
		switch field.Kind() {
		case
			protoreflect.BoolKind,
			protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
			protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
			protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
			protoreflect.Uint64Kind, protoreflect.Fixed64Kind,
			protoreflect.StringKind:
			if field.IsList() {
				return fmt.Errorf("tag %d: %s: lists unsupported", field.Number(), field.Name())
			}
			EventFields = append(EventFields, &EventField{
				Type: int32(field.Kind()),
				Tag:  int32(field.Number()),
			})
		default:
			return fmt.Errorf("tag %d: %s: unsupported kind %q", field.Number(), field.Name(), field.Kind().String())
		}
	}
	return nil
}

func init() {
	if err := initEventFields(); err != nil {
		panic(fmt.Errorf("init event fields: %w", err))
	}
}
