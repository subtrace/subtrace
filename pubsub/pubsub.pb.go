// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.34.2
// 	protoc        v3.21.12
// source: pubsub/pubsub.proto

package pubsub

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// POST /api/JoinPublisher
type JoinPublisher struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *JoinPublisher) Reset() {
	*x = JoinPublisher{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pubsub_pubsub_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JoinPublisher) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JoinPublisher) ProtoMessage() {}

func (x *JoinPublisher) ProtoReflect() protoreflect.Message {
	mi := &file_pubsub_pubsub_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JoinPublisher.ProtoReflect.Descriptor instead.
func (*JoinPublisher) Descriptor() ([]byte, []int) {
	return file_pubsub_pubsub_proto_rawDescGZIP(), []int{0}
}

// POST /api/JoinSubscriber
type JoinSubscriber struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *JoinSubscriber) Reset() {
	*x = JoinSubscriber{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pubsub_pubsub_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JoinSubscriber) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JoinSubscriber) ProtoMessage() {}

func (x *JoinSubscriber) ProtoReflect() protoreflect.Message {
	mi := &file_pubsub_pubsub_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JoinSubscriber.ProtoReflect.Descriptor instead.
func (*JoinSubscriber) Descriptor() ([]byte, []int) {
	return file_pubsub_pubsub_proto_rawDescGZIP(), []int{1}
}

type Event struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Types that are assignable to Concrete:
	//
	//	*Event_ConcreteV1
	Concrete isEvent_Concrete `protobuf_oneof:"concrete"`
}

func (x *Event) Reset() {
	*x = Event{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pubsub_pubsub_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *Event) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Event) ProtoMessage() {}

func (x *Event) ProtoReflect() protoreflect.Message {
	mi := &file_pubsub_pubsub_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Event.ProtoReflect.Descriptor instead.
func (*Event) Descriptor() ([]byte, []int) {
	return file_pubsub_pubsub_proto_rawDescGZIP(), []int{2}
}

func (m *Event) GetConcrete() isEvent_Concrete {
	if m != nil {
		return m.Concrete
	}
	return nil
}

func (x *Event) GetConcreteV1() *Event_V1 {
	if x, ok := x.GetConcrete().(*Event_ConcreteV1); ok {
		return x.ConcreteV1
	}
	return nil
}

type isEvent_Concrete interface {
	isEvent_Concrete()
}

type Event_ConcreteV1 struct {
	ConcreteV1 *Event_V1 `protobuf:"bytes,1,opt,name=concrete_v1,json=concreteV1,proto3,oneof"`
}

func (*Event_ConcreteV1) isEvent_Concrete() {}

type Message struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Types that are assignable to Concrete:
	//
	//	*Message_ConcreteV1
	Concrete isMessage_Concrete `protobuf_oneof:"concrete"`
}

func (x *Message) Reset() {
	*x = Message{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pubsub_pubsub_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *Message) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Message) ProtoMessage() {}

func (x *Message) ProtoReflect() protoreflect.Message {
	mi := &file_pubsub_pubsub_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Message.ProtoReflect.Descriptor instead.
func (*Message) Descriptor() ([]byte, []int) {
	return file_pubsub_pubsub_proto_rawDescGZIP(), []int{3}
}

func (m *Message) GetConcrete() isMessage_Concrete {
	if m != nil {
		return m.Concrete
	}
	return nil
}

func (x *Message) GetConcreteV1() *Message_V1 {
	if x, ok := x.GetConcrete().(*Message_ConcreteV1); ok {
		return x.ConcreteV1
	}
	return nil
}

type isMessage_Concrete interface {
	isMessage_Concrete()
}

type Message_ConcreteV1 struct {
	ConcreteV1 *Message_V1 `protobuf:"bytes,1,opt,name=concrete_v1,json=concreteV1,proto3,oneof"`
}

func (*Message_ConcreteV1) isMessage_Concrete() {}

type JoinPublisher_Request struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *JoinPublisher_Request) Reset() {
	*x = JoinPublisher_Request{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pubsub_pubsub_proto_msgTypes[4]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JoinPublisher_Request) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JoinPublisher_Request) ProtoMessage() {}

func (x *JoinPublisher_Request) ProtoReflect() protoreflect.Message {
	mi := &file_pubsub_pubsub_proto_msgTypes[4]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JoinPublisher_Request.ProtoReflect.Descriptor instead.
func (*JoinPublisher_Request) Descriptor() ([]byte, []int) {
	return file_pubsub_pubsub_proto_rawDescGZIP(), []int{0, 0}
}

type JoinPublisher_Response struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Error        *string `protobuf:"bytes,1000,opt,name=error,proto3,oneof" json:"error,omitempty"`
	WebsocketUrl string  `protobuf:"bytes,1,opt,name=websocket_url,json=websocketUrl,proto3" json:"websocket_url,omitempty"`
}

func (x *JoinPublisher_Response) Reset() {
	*x = JoinPublisher_Response{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pubsub_pubsub_proto_msgTypes[5]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JoinPublisher_Response) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JoinPublisher_Response) ProtoMessage() {}

func (x *JoinPublisher_Response) ProtoReflect() protoreflect.Message {
	mi := &file_pubsub_pubsub_proto_msgTypes[5]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JoinPublisher_Response.ProtoReflect.Descriptor instead.
func (*JoinPublisher_Response) Descriptor() ([]byte, []int) {
	return file_pubsub_pubsub_proto_rawDescGZIP(), []int{0, 1}
}

func (x *JoinPublisher_Response) GetError() string {
	if x != nil && x.Error != nil {
		return *x.Error
	}
	return ""
}

func (x *JoinPublisher_Response) GetWebsocketUrl() string {
	if x != nil {
		return x.WebsocketUrl
	}
	return ""
}

type JoinSubscriber_Request struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	NamespaceId string `protobuf:"bytes,1,opt,name=namespace_id,json=namespaceId,proto3" json:"namespace_id,omitempty"`
}

func (x *JoinSubscriber_Request) Reset() {
	*x = JoinSubscriber_Request{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pubsub_pubsub_proto_msgTypes[6]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JoinSubscriber_Request) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JoinSubscriber_Request) ProtoMessage() {}

func (x *JoinSubscriber_Request) ProtoReflect() protoreflect.Message {
	mi := &file_pubsub_pubsub_proto_msgTypes[6]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JoinSubscriber_Request.ProtoReflect.Descriptor instead.
func (*JoinSubscriber_Request) Descriptor() ([]byte, []int) {
	return file_pubsub_pubsub_proto_rawDescGZIP(), []int{1, 0}
}

func (x *JoinSubscriber_Request) GetNamespaceId() string {
	if x != nil {
		return x.NamespaceId
	}
	return ""
}

type JoinSubscriber_Response struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Error        *string `protobuf:"bytes,1000,opt,name=error,proto3,oneof" json:"error,omitempty"`
	WebsocketUrl string  `protobuf:"bytes,1,opt,name=websocket_url,json=websocketUrl,proto3" json:"websocket_url,omitempty"`
}

func (x *JoinSubscriber_Response) Reset() {
	*x = JoinSubscriber_Response{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pubsub_pubsub_proto_msgTypes[7]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *JoinSubscriber_Response) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*JoinSubscriber_Response) ProtoMessage() {}

func (x *JoinSubscriber_Response) ProtoReflect() protoreflect.Message {
	mi := &file_pubsub_pubsub_proto_msgTypes[7]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use JoinSubscriber_Response.ProtoReflect.Descriptor instead.
func (*JoinSubscriber_Response) Descriptor() ([]byte, []int) {
	return file_pubsub_pubsub_proto_rawDescGZIP(), []int{1, 1}
}

func (x *JoinSubscriber_Response) GetError() string {
	if x != nil && x.Error != nil {
		return *x.Error
	}
	return ""
}

func (x *JoinSubscriber_Response) GetWebsocketUrl() string {
	if x != nil {
		return x.WebsocketUrl
	}
	return ""
}

type Event_V1 struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Tags         map[string]string `protobuf:"bytes,1,rep,name=tags,proto3" json:"tags,omitempty" protobuf_key:"bytes,1,opt,name=key,proto3" protobuf_val:"bytes,2,opt,name=value,proto3"`
	HarEntryJson []byte            `protobuf:"bytes,2,opt,name=har_entry_json,json=harEntryJson,proto3" json:"har_entry_json,omitempty"`
}

func (x *Event_V1) Reset() {
	*x = Event_V1{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pubsub_pubsub_proto_msgTypes[8]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *Event_V1) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Event_V1) ProtoMessage() {}

func (x *Event_V1) ProtoReflect() protoreflect.Message {
	mi := &file_pubsub_pubsub_proto_msgTypes[8]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Event_V1.ProtoReflect.Descriptor instead.
func (*Event_V1) Descriptor() ([]byte, []int) {
	return file_pubsub_pubsub_proto_rawDescGZIP(), []int{2, 0}
}

func (x *Event_V1) GetTags() map[string]string {
	if x != nil {
		return x.Tags
	}
	return nil
}

func (x *Event_V1) GetHarEntryJson() []byte {
	if x != nil {
		return x.HarEntryJson
	}
	return nil
}

type Message_V1 struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Types that are assignable to Underlying:
	//
	//	*Message_V1_Event
	Underlying isMessage_V1_Underlying `protobuf_oneof:"underlying"`
}

func (x *Message_V1) Reset() {
	*x = Message_V1{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pubsub_pubsub_proto_msgTypes[10]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *Message_V1) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Message_V1) ProtoMessage() {}

func (x *Message_V1) ProtoReflect() protoreflect.Message {
	mi := &file_pubsub_pubsub_proto_msgTypes[10]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Message_V1.ProtoReflect.Descriptor instead.
func (*Message_V1) Descriptor() ([]byte, []int) {
	return file_pubsub_pubsub_proto_rawDescGZIP(), []int{3, 0}
}

func (m *Message_V1) GetUnderlying() isMessage_V1_Underlying {
	if m != nil {
		return m.Underlying
	}
	return nil
}

func (x *Message_V1) GetEvent() *Event {
	if x, ok := x.GetUnderlying().(*Message_V1_Event); ok {
		return x.Event
	}
	return nil
}

type isMessage_V1_Underlying interface {
	isMessage_V1_Underlying()
}

type Message_V1_Event struct {
	Event *Event `protobuf:"bytes,1,opt,name=event,proto3,oneof"`
}

func (*Message_V1_Event) isMessage_V1_Underlying() {}

var File_pubsub_pubsub_proto protoreflect.FileDescriptor

var file_pubsub_pubsub_proto_rawDesc = []byte{
	0x0a, 0x13, 0x70, 0x75, 0x62, 0x73, 0x75, 0x62, 0x2f, 0x70, 0x75, 0x62, 0x73, 0x75, 0x62, 0x2e,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x0f, 0x73, 0x75, 0x62, 0x74, 0x72, 0x61, 0x63, 0x65, 0x2e,
	0x70, 0x75, 0x62, 0x73, 0x75, 0x62, 0x22, 0x71, 0x0a, 0x0d, 0x4a, 0x6f, 0x69, 0x6e, 0x50, 0x75,
	0x62, 0x6c, 0x69, 0x73, 0x68, 0x65, 0x72, 0x1a, 0x09, 0x0a, 0x07, 0x52, 0x65, 0x71, 0x75, 0x65,
	0x73, 0x74, 0x1a, 0x55, 0x0a, 0x08, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x1a,
	0x0a, 0x05, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x18, 0xe8, 0x07, 0x20, 0x01, 0x28, 0x09, 0x48, 0x00,
	0x52, 0x05, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x88, 0x01, 0x01, 0x12, 0x23, 0x0a, 0x0d, 0x77, 0x65,
	0x62, 0x73, 0x6f, 0x63, 0x6b, 0x65, 0x74, 0x5f, 0x75, 0x72, 0x6c, 0x18, 0x01, 0x20, 0x01, 0x28,
	0x09, 0x52, 0x0c, 0x77, 0x65, 0x62, 0x73, 0x6f, 0x63, 0x6b, 0x65, 0x74, 0x55, 0x72, 0x6c, 0x42,
	0x08, 0x0a, 0x06, 0x5f, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x22, 0x95, 0x01, 0x0a, 0x0e, 0x4a, 0x6f,
	0x69, 0x6e, 0x53, 0x75, 0x62, 0x73, 0x63, 0x72, 0x69, 0x62, 0x65, 0x72, 0x1a, 0x2c, 0x0a, 0x07,
	0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x21, 0x0a, 0x0c, 0x6e, 0x61, 0x6d, 0x65, 0x73,
	0x70, 0x61, 0x63, 0x65, 0x5f, 0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0b, 0x6e,
	0x61, 0x6d, 0x65, 0x73, 0x70, 0x61, 0x63, 0x65, 0x49, 0x64, 0x1a, 0x55, 0x0a, 0x08, 0x52, 0x65,
	0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x1a, 0x0a, 0x05, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x18,
	0xe8, 0x07, 0x20, 0x01, 0x28, 0x09, 0x48, 0x00, 0x52, 0x05, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x88,
	0x01, 0x01, 0x12, 0x23, 0x0a, 0x0d, 0x77, 0x65, 0x62, 0x73, 0x6f, 0x63, 0x6b, 0x65, 0x74, 0x5f,
	0x75, 0x72, 0x6c, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0c, 0x77, 0x65, 0x62, 0x73, 0x6f,
	0x63, 0x6b, 0x65, 0x74, 0x55, 0x72, 0x6c, 0x42, 0x08, 0x0a, 0x06, 0x5f, 0x65, 0x72, 0x72, 0x6f,
	0x72, 0x22, 0xf0, 0x01, 0x0a, 0x05, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x12, 0x3c, 0x0a, 0x0b, 0x63,
	0x6f, 0x6e, 0x63, 0x72, 0x65, 0x74, 0x65, 0x5f, 0x76, 0x31, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b,
	0x32, 0x19, 0x2e, 0x73, 0x75, 0x62, 0x74, 0x72, 0x61, 0x63, 0x65, 0x2e, 0x70, 0x75, 0x62, 0x73,
	0x75, 0x62, 0x2e, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x2e, 0x56, 0x31, 0x48, 0x00, 0x52, 0x0a, 0x63,
	0x6f, 0x6e, 0x63, 0x72, 0x65, 0x74, 0x65, 0x56, 0x31, 0x1a, 0x9c, 0x01, 0x0a, 0x02, 0x56, 0x31,
	0x12, 0x37, 0x0a, 0x04, 0x74, 0x61, 0x67, 0x73, 0x18, 0x01, 0x20, 0x03, 0x28, 0x0b, 0x32, 0x23,
	0x2e, 0x73, 0x75, 0x62, 0x74, 0x72, 0x61, 0x63, 0x65, 0x2e, 0x70, 0x75, 0x62, 0x73, 0x75, 0x62,
	0x2e, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x2e, 0x56, 0x31, 0x2e, 0x54, 0x61, 0x67, 0x73, 0x45, 0x6e,
	0x74, 0x72, 0x79, 0x52, 0x04, 0x74, 0x61, 0x67, 0x73, 0x12, 0x24, 0x0a, 0x0e, 0x68, 0x61, 0x72,
	0x5f, 0x65, 0x6e, 0x74, 0x72, 0x79, 0x5f, 0x6a, 0x73, 0x6f, 0x6e, 0x18, 0x02, 0x20, 0x01, 0x28,
	0x0c, 0x52, 0x0c, 0x68, 0x61, 0x72, 0x45, 0x6e, 0x74, 0x72, 0x79, 0x4a, 0x73, 0x6f, 0x6e, 0x1a,
	0x37, 0x0a, 0x09, 0x54, 0x61, 0x67, 0x73, 0x45, 0x6e, 0x74, 0x72, 0x79, 0x12, 0x10, 0x0a, 0x03,
	0x6b, 0x65, 0x79, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x03, 0x6b, 0x65, 0x79, 0x12, 0x14,
	0x0a, 0x05, 0x76, 0x61, 0x6c, 0x75, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x05, 0x76,
	0x61, 0x6c, 0x75, 0x65, 0x3a, 0x02, 0x38, 0x01, 0x42, 0x0a, 0x0a, 0x08, 0x63, 0x6f, 0x6e, 0x63,
	0x72, 0x65, 0x74, 0x65, 0x22, 0x99, 0x01, 0x0a, 0x07, 0x4d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65,
	0x12, 0x3e, 0x0a, 0x0b, 0x63, 0x6f, 0x6e, 0x63, 0x72, 0x65, 0x74, 0x65, 0x5f, 0x76, 0x31, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x1b, 0x2e, 0x73, 0x75, 0x62, 0x74, 0x72, 0x61, 0x63, 0x65,
	0x2e, 0x70, 0x75, 0x62, 0x73, 0x75, 0x62, 0x2e, 0x4d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x2e,
	0x56, 0x31, 0x48, 0x00, 0x52, 0x0a, 0x63, 0x6f, 0x6e, 0x63, 0x72, 0x65, 0x74, 0x65, 0x56, 0x31,
	0x1a, 0x42, 0x0a, 0x02, 0x56, 0x31, 0x12, 0x2e, 0x0a, 0x05, 0x65, 0x76, 0x65, 0x6e, 0x74, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x16, 0x2e, 0x73, 0x75, 0x62, 0x74, 0x72, 0x61, 0x63, 0x65,
	0x2e, 0x70, 0x75, 0x62, 0x73, 0x75, 0x62, 0x2e, 0x45, 0x76, 0x65, 0x6e, 0x74, 0x48, 0x00, 0x52,
	0x05, 0x65, 0x76, 0x65, 0x6e, 0x74, 0x42, 0x0c, 0x0a, 0x0a, 0x75, 0x6e, 0x64, 0x65, 0x72, 0x6c,
	0x79, 0x69, 0x6e, 0x67, 0x42, 0x0a, 0x0a, 0x08, 0x63, 0x6f, 0x6e, 0x63, 0x72, 0x65, 0x74, 0x65,
	0x42, 0x15, 0x5a, 0x13, 0x73, 0x75, 0x62, 0x74, 0x72, 0x61, 0x63, 0x65, 0x2e, 0x64, 0x65, 0x76,
	0x2f, 0x70, 0x75, 0x62, 0x73, 0x75, 0x62, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_pubsub_pubsub_proto_rawDescOnce sync.Once
	file_pubsub_pubsub_proto_rawDescData = file_pubsub_pubsub_proto_rawDesc
)

func file_pubsub_pubsub_proto_rawDescGZIP() []byte {
	file_pubsub_pubsub_proto_rawDescOnce.Do(func() {
		file_pubsub_pubsub_proto_rawDescData = protoimpl.X.CompressGZIP(file_pubsub_pubsub_proto_rawDescData)
	})
	return file_pubsub_pubsub_proto_rawDescData
}

var file_pubsub_pubsub_proto_msgTypes = make([]protoimpl.MessageInfo, 11)
var file_pubsub_pubsub_proto_goTypes = []any{
	(*JoinPublisher)(nil),           // 0: subtrace.pubsub.JoinPublisher
	(*JoinSubscriber)(nil),          // 1: subtrace.pubsub.JoinSubscriber
	(*Event)(nil),                   // 2: subtrace.pubsub.Event
	(*Message)(nil),                 // 3: subtrace.pubsub.Message
	(*JoinPublisher_Request)(nil),   // 4: subtrace.pubsub.JoinPublisher.Request
	(*JoinPublisher_Response)(nil),  // 5: subtrace.pubsub.JoinPublisher.Response
	(*JoinSubscriber_Request)(nil),  // 6: subtrace.pubsub.JoinSubscriber.Request
	(*JoinSubscriber_Response)(nil), // 7: subtrace.pubsub.JoinSubscriber.Response
	(*Event_V1)(nil),                // 8: subtrace.pubsub.Event.V1
	nil,                             // 9: subtrace.pubsub.Event.V1.TagsEntry
	(*Message_V1)(nil),              // 10: subtrace.pubsub.Message.V1
}
var file_pubsub_pubsub_proto_depIdxs = []int32{
	8,  // 0: subtrace.pubsub.Event.concrete_v1:type_name -> subtrace.pubsub.Event.V1
	10, // 1: subtrace.pubsub.Message.concrete_v1:type_name -> subtrace.pubsub.Message.V1
	9,  // 2: subtrace.pubsub.Event.V1.tags:type_name -> subtrace.pubsub.Event.V1.TagsEntry
	2,  // 3: subtrace.pubsub.Message.V1.event:type_name -> subtrace.pubsub.Event
	4,  // [4:4] is the sub-list for method output_type
	4,  // [4:4] is the sub-list for method input_type
	4,  // [4:4] is the sub-list for extension type_name
	4,  // [4:4] is the sub-list for extension extendee
	0,  // [0:4] is the sub-list for field type_name
}

func init() { file_pubsub_pubsub_proto_init() }
func file_pubsub_pubsub_proto_init() {
	if File_pubsub_pubsub_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_pubsub_pubsub_proto_msgTypes[0].Exporter = func(v any, i int) any {
			switch v := v.(*JoinPublisher); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_pubsub_pubsub_proto_msgTypes[1].Exporter = func(v any, i int) any {
			switch v := v.(*JoinSubscriber); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_pubsub_pubsub_proto_msgTypes[2].Exporter = func(v any, i int) any {
			switch v := v.(*Event); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_pubsub_pubsub_proto_msgTypes[3].Exporter = func(v any, i int) any {
			switch v := v.(*Message); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_pubsub_pubsub_proto_msgTypes[4].Exporter = func(v any, i int) any {
			switch v := v.(*JoinPublisher_Request); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_pubsub_pubsub_proto_msgTypes[5].Exporter = func(v any, i int) any {
			switch v := v.(*JoinPublisher_Response); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_pubsub_pubsub_proto_msgTypes[6].Exporter = func(v any, i int) any {
			switch v := v.(*JoinSubscriber_Request); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_pubsub_pubsub_proto_msgTypes[7].Exporter = func(v any, i int) any {
			switch v := v.(*JoinSubscriber_Response); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_pubsub_pubsub_proto_msgTypes[8].Exporter = func(v any, i int) any {
			switch v := v.(*Event_V1); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_pubsub_pubsub_proto_msgTypes[10].Exporter = func(v any, i int) any {
			switch v := v.(*Message_V1); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	file_pubsub_pubsub_proto_msgTypes[2].OneofWrappers = []any{
		(*Event_ConcreteV1)(nil),
	}
	file_pubsub_pubsub_proto_msgTypes[3].OneofWrappers = []any{
		(*Message_ConcreteV1)(nil),
	}
	file_pubsub_pubsub_proto_msgTypes[5].OneofWrappers = []any{}
	file_pubsub_pubsub_proto_msgTypes[7].OneofWrappers = []any{}
	file_pubsub_pubsub_proto_msgTypes[10].OneofWrappers = []any{
		(*Message_V1_Event)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_pubsub_pubsub_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   11,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_pubsub_pubsub_proto_goTypes,
		DependencyIndexes: file_pubsub_pubsub_proto_depIdxs,
		MessageInfos:      file_pubsub_pubsub_proto_msgTypes,
	}.Build()
	File_pubsub_pubsub_proto = out.File
	file_pubsub_pubsub_proto_rawDesc = nil
	file_pubsub_pubsub_proto_goTypes = nil
	file_pubsub_pubsub_proto_depIdxs = nil
}
