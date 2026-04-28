package sqs_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/stretchr/testify/assert"

	db "sigmaos/debug"
	"sigmaos/test"
	"sigmaos/util/rand"
)

var QUEUE_URL string = "https://sqs.us-east-1.amazonaws.com/223652007189/sigmaos-test-queue.fifo"

func TestCompile(t *testing.T) {
}

func TestSendRecvDeleteDirect(t *testing.T) {
	cfg, err := config.LoadDefaultConfig(context.TODO(), config.WithSharedConfigProfile("sigmaos"))
	assert.Nil(t, err, "Err load config: %v", err)
	clnt := sqs.NewFromConfig(cfg)
	msgStr := "Hello 1"
	msgGrpID := "test-msg-grp-id"
	msgDedupID := "test-msg-dedup-id-" + rand.String(4)
	start := time.Now()
	sendOut, err := clnt.SendMessage(context.TODO(), &sqs.SendMessageInput{
		QueueUrl:               &QUEUE_URL,
		MessageBody:            &msgStr,
		MessageGroupId:         &msgGrpID,
		MessageDeduplicationId: &msgDedupID,
	})
	if !assert.Nil(t, err, "Err SendMessage: %v", err) {
		return
	}
	db.DPrintf(db.TEST, "Send message result id:%v seqno:%v", sendOut.MessageId, sendOut.SequenceNumber)
	db.DPrintf(db.TEST, "Send lat=%v", time.Since(start))
	start = time.Now()
	recvOut, err := clnt.ReceiveMessage(context.TODO(), &sqs.ReceiveMessageInput{
		QueueUrl: &QUEUE_URL,
	})
	assert.Nil(t, err, "Err ReceiveMessage: %v", err)
	if !assert.Equal(t, 1, len(recvOut.Messages), "Unexpected number of messages: %v", len(recvOut.Messages)) {
		return
	}
	msg := recvOut.Messages[0]
	db.DPrintf(db.TEST, "Recv message id:%v seqno:%v", msg.MessageId, msg.ReceiptHandle)
	db.DPrintf(db.TEST, "Recv lat=%v", time.Since(start))
	start = time.Now()
	_, err = clnt.DeleteMessage(context.TODO(), &sqs.DeleteMessageInput{
		QueueUrl:      &QUEUE_URL,
		ReceiptHandle: msg.ReceiptHandle,
	})
	assert.Nil(t, err, "Err DeleteMessage: %v", err)
	db.DPrintf(db.TEST, "Delete message id:%v seqno:%v receiptHandle:%v", sendOut.MessageId, sendOut.SequenceNumber, msg.ReceiptHandle)
	db.DPrintf(db.TEST, "Delete lat=%v", time.Since(start))
}

func TestSQSProxy(t *testing.T) {
	if false {
		ts, err1 := test.NewTstateAll(t)
		if !assert.Nil(ts.T, err1, "Error New Tstate: %v", err1) {
			return
		}
	}
}
