package route

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"compress/gzip"

	"github.com/apache/pulsar-client-go/pulsar"
	"github.com/gorilla/mux"
	"github.com/kafkaesque-io/pulsar-beam/src/broker"
	"github.com/kafkaesque-io/pulsar-beam/src/db"
	"github.com/kafkaesque-io/pulsar-beam/src/model"
	"github.com/kafkaesque-io/pulsar-beam/src/pulsardriver"
	"github.com/kafkaesque-io/pulsar-beam/src/util"

	log "github.com/sirupsen/logrus"
)

var singleDb db.Db

const subDelimiter = "-"

// 5MB + 1 byte buffer (default Pulsar message size limit is 5MB https://pulsar.apache.org/docs/concepts-messaging/)
const workerBufferSize = 5242881

var workerPool chan func(buffer []byte)

// Init initializes database
func Init() {
	singleDb = db.NewDbWithPanic(util.GetConfig().PbDbType)
	
	log.Infof("Start worker pool with size = %d", util.GetConfig().WorkerPoolSize)
	workerPool = make(chan func(buffer []byte), util.GetConfig().WorkerPoolSize)
	
	// Start a number of goroutine as worker pool
	for i := 0; i < util.GetConfig().WorkerPoolSize; i++ {
		go func() {
			var buffer [workerBufferSize]byte
			for f := range workerPool {
				f(buffer[:])
			}
		}()
	}
}

// TokenServerResponse is the json object for token server response
type TokenServerResponse struct {
	Subject string `json:"subject"`
	Token   string `json:"token"`
}

// TokenSubjectHandler issues new token
func TokenSubjectHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	subject, ok := vars["sub"]
	if !ok {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	if util.StrContains(util.SuperRoles, util.AssignString(r.Header.Get("injectedSubs"), "BOGUSROLE")) {
		tokenString, err := util.JWTAuth.GenerateToken(subject)
		if err != nil {
			util.ResponseErrorJSON(errors.New("failed to generate token"), w, http.StatusInternalServerError)
		} else {
			respJSON, err := json.Marshal(&TokenServerResponse{
				Subject: subject,
				Token:   tokenString,
			})
			if err != nil {
				util.ResponseErrorJSON(errors.New("failed to marshal token response json object"), w, http.StatusInternalServerError)
				return
			}
			w.Write(respJSON)
		}
		return
	}
	util.ResponseErrorJSON(errors.New("incorrect subject"), w, http.StatusUnauthorized)
	return
}

// StatusPage replies with basic status code
func StatusPage(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	return
}

// ReceiveHandler - the message receiver handler
func ReceiveHandler(w http.ResponseWriter, r *http.Request) {
	done := make(chan bool)
	workerPool <- func(buffer []byte) {
		var b []byte = buffer[:0]
		var err error
		var bufferSize int = 0
		
		defer r.Body.Close()
		defer func() { done <- true }()
        
        // Include request line (GET /uri HTTP/1.1) into the message payload if url has includeRequestLine=true
		includeRequestLine, isIncludeRequestLine := r.URL.Query()["includeRequestLine"]
		
		if isIncludeRequestLine && includeRequestLine[0] != "false"  {
			b = append(append(append(append(append(append(b, r.Method...), " "...), r.RequestURI...), " "...), r.Proto...), "\r\n"...)
		}
		
		// Include headers information into the message payload if url has includeHeaders=true
		includeHeaders, isIncludeHeaders := r.URL.Query()["includeHeaders"]
		
		if isIncludeHeaders && includeHeaders[0] != "false"  {
			for name, values := range r.Header {
				b = append(append(append(append(b, name...), ": "...), values[0]...), "\r\n"...)
			}
		}
        
        // Append header delimiter (\r\n\r\n) and adjust the buffer size
        if isIncludeRequestLine || isIncludeHeaders {
            b = append(b, "\r\n\r\n"...)
            bufferSize = len(b)
        }
		
		if r.Header.Get("Content-Encoding") == "gzip" {
			g, gerr := gzip.NewReader(r.Body)
			
			if gerr != nil {
				util.ResponseErrorJSON(gerr, w, http.StatusInternalServerError)
				return
			}
			
			defer g.Close()
			
            var n int
			for {
                n, err = g.Read(buffer[bufferSize:])
                bufferSize += n
                if err == io.EOF {
                    break
                } else if err != nil {
                    util.ResponseErrorJSON(err, w, http.StatusInternalServerError)
                    return
                } else if bufferSize >= workerBufferSize {
                    util.ResponseErrorJSON(errors.New("Buffer overflow"), w, http.StatusInternalServerError)
                    return
                }
            }
		} else {
            var n int
            for {
                n, err = r.Body.Read(buffer[bufferSize:])
                bufferSize += n
                if err == io.EOF {
                    break
                } else if err != nil {
                    util.ResponseErrorJSON(err, w, http.StatusInternalServerError)
                    return
                } else if bufferSize >= workerBufferSize {
                    util.ResponseErrorJSON(errors.New("Buffer overflow"), w, http.StatusInternalServerError)
                    return
                }
            }
		}
		
		b = buffer[:bufferSize]
		log.Debugf("Message buffer (size = %d): %s", bufferSize, b);
		
		token, topic, pulsarURL, err := util.ReceiverHeader(util.AllowedPulsarURLs, &r.Header)
		if err != nil {
			util.ResponseErrorJSON(err, w, http.StatusUnauthorized)
			return
		}
		
		topicFN, err2 := GetTopicFnFromRoute(mux.Vars(r))
		if topic == "" && err2 != nil {
			// only read topic from routes
			util.ResponseErrorJSON(err2, w, http.StatusUnprocessableEntity)
			return
		}
		topicFN = util.AssignString(topic, topicFN) // header topicFn overwrites topic specified in the routes
		log.Infof("topicFN %s pulsarURL %s", topicFN, pulsarURL)

		pulsarAsync := r.URL.Query().Get("mode") == "async"
		err = pulsardriver.SendToPulsar(pulsarURL, token, topicFN, b, pulsarAsync, false, 0)
		if err != nil {
			util.ResponseErrorJSON(err, w, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	<-done
	return
}

// recoverHandler a function recovers from panic
func recoverHandler(r *http.Request) {
	if r := recover(); r != nil {
		fmt.Printf("Recovered in http handler crash %v", r)
	} else {
		fmt.Printf("exit http handler")
	}
}

// PollHandler polls messages from a Pulsar topic.
func PollHandler(w http.ResponseWriter, r *http.Request) {
	defer recoverHandler(r)

	u, _ := url.Parse(r.URL.String())
	params := u.Query()
	token, topicFN, pulsarURL, subName, _, subType, err := ConsumerConfigFromHTTPParts(util.AllowedPulsarURLs, &r.Header, mux.Vars(r), params)
	if err != nil {
		util.ResponseErrorJSON(err, w, http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")

	size := util.QueryParamInt(params, "batchSize", 10)
	perMessageTimeoutMs := util.QueryParamInt(params, "perMessageTimeoutMs", 300)

	// subscription initial position is always set to earliest since this is short poll
	msgs, err := broker.PollBatchMessages(pulsarURL, token, topicFN, subName, subType, size, perMessageTimeoutMs)
	if err != nil {
		util.ResponseErrorJSON(err, w, http.StatusInternalServerError)
		return
	}

	if msgs.IsEmpty() {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	data, err := json.Marshal(msgs)
	if err != nil {
		util.ResponseErrorJSON(err, w, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// SSEHandler is the HTTP SSE handler
func SSEHandler(w http.ResponseWriter, r *http.Request) {
	defer recoverHandler(r)

	u, _ := url.Parse(r.URL.String())
	params := u.Query()
	token, topicFN, pulsarURL, subName, subInitPos, subType, err := ConsumerConfigFromHTTPParts(util.AllowedPulsarURLs, &r.Header, mux.Vars(r), params)
	if err != nil {
		util.ResponseErrorJSON(err, w, http.StatusUnprocessableEntity)
		return
	}

	// Make sure that the writer supports flushing.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*") // allow connection from different domain

	client, consumer, err := broker.GetPulsarClientConsumer(pulsarURL, token, topicFN, subName, subType, subInitPos)
	if err != nil {
		util.ResponseErrorJSON(err, w, http.StatusInternalServerError)
		return
	}
	defer client.Close()
	defer consumer.Close()
	if strings.HasPrefix(subName, model.NonResumable) {
		defer consumer.Unsubscribe()
	}

	consumChan := consumer.Chan()
	for {
		select {
		case msg := <-consumChan:
			// log.Infof("received message %s on topic %s", string(msg.Payload()), topicFN)
			consumer.Ack(msg)

			// ledgerId, entryId, batchId, partitionIndex, reserved, consumerId
			fmt.Fprintf(w, strings.Replace(fmt.Sprintf("id: %v\n", msg.Message.ID()), "&", "", 1))
			fmt.Fprintf(w, "data: %s\n\n", msg.Payload())
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// GetTopicHandler gets the topic details
func GetTopicHandler(w http.ResponseWriter, r *http.Request) {
	topicKey, err := GetTopicKey(r)
	if err != nil {
		util.ResponseErrorJSON(err, w, http.StatusUnprocessableEntity)
		return
	}

	// TODO: we may fix the problem that allows negatively look up by another tenant
	doc, err := singleDb.GetByKey(topicKey)
	if err != nil {
		log.Errorf("get topic error %v", err)
		util.ResponseErrorJSON(err, w, http.StatusNotFound)
		return
	}
	if !VerifySubjectBasedOnTopic(doc.TopicFullName, r.Header.Get("injectedSubs"), ExtractEvalTenant) {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	resJSON, err := json.Marshal(doc)
	if err != nil {
		util.ResponseErrorJSON(err, w, http.StatusInternalServerError)
	} else {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.Write(resJSON)
	}

}

// UpdateTopicHandler creates or updates a topic
func UpdateTopicHandler(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	defer r.Body.Close()
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")

	var doc model.TopicConfig
	err := decoder.Decode(&doc)
	if err != nil {
		util.ResponseErrorJSON(err, w, http.StatusUnprocessableEntity)
		return
	}

	if _, err = model.ValidateTopicConfig(doc); err != nil {
		util.ResponseErrorJSON(err, w, http.StatusUnprocessableEntity)
		return
	}

	if !VerifySubjectBasedOnTopic(doc.TopicFullName, r.Header.Get("injectedSubs"), ExtractEvalTenant) {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	id, err := singleDb.Update(&doc)
	if err != nil {
		util.ResponseErrorJSON(err, w, http.StatusConflict)
		return
	}
	if len(id) > 1 {
		savedDoc, err := singleDb.GetByKey(id)
		if err != nil {
			util.ResponseErrorJSON(err, w, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		resJSON, err := json.Marshal(savedDoc)
		if err != nil {
			util.ResponseErrorJSON(err, w, http.StatusInternalServerError)
			return
		}
		w.Write(resJSON)
		return
	}
	util.ResponseErrorJSON(fmt.Errorf("failed to update"), w, http.StatusInternalServerError)
	return
}

// DeleteTopicHandler deletes a topic
func DeleteTopicHandler(w http.ResponseWriter, r *http.Request) {
	topicKey, err := GetTopicKey(r)
	if err != nil {
		util.ResponseErrorJSON(err, w, http.StatusUnprocessableEntity)
		return
	}

	doc, err := singleDb.GetByKey(topicKey)
	if err != nil {
		log.Errorf("failed to get topic based on key %s err: %v", topicKey, err)
		util.ResponseErrorJSON(err, w, http.StatusNotFound)
		return
	}
	if !VerifySubjectBasedOnTopic(doc.TopicFullName, r.Header.Get("injectedSubs"), ExtractEvalTenant) {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	deletedKey, err := singleDb.DeleteByKey(topicKey)
	if err != nil {
		util.ResponseErrorJSON(err, w, http.StatusNotFound)
		return
	}
	resJSON, err := json.Marshal(deletedKey)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.Write(resJSON)
	}
}

// GetTopicKey gets the topic key from the request body or url sub route
func GetTopicKey(r *http.Request) (string, error) {
	var err error
	vars := mux.Vars(r)
	topicKey, ok := vars["topicKey"]
	if !ok {
		var topic model.TopicKey
		decoder := json.NewDecoder(r.Body)
		defer r.Body.Close()

		err := decoder.Decode(&topic)
		switch {
		case err == io.EOF:
			return "", errors.New("missing topic key or topic names in body")
		case err != nil:
			return "", err
		}
		topicKey, err = model.GetKeyFromNames(topic.TopicFullName, topic.PulsarURL)
		if err != nil {
			return "", err
		}
	}
	return topicKey, err
}

// VerifySubjectBasedOnTopic verifies the subject can meet the requirement.
func VerifySubjectBasedOnTopic(topicFN, tokenSub string, evalTenant func(tenant, subjects string) bool) bool {
	parts := strings.Split(topicFN, "/")
	if len(parts) < 4 {
		return false
	}
	tenant := parts[2]
	if len(tenant) < 1 {
		log.Infof(" auth verify tenant %s token sub %s", tenant, tokenSub)
		return false
	}
	return VerifySubject(tenant, tokenSub, evalTenant)
}

// VerifySubject verifies the subject can meet the requirement.
// Subject verification requires role or tenant name in the jwt subject
func VerifySubject(requiredSubject, tokenSubjects string, evalTenant func(tenant, subjects string) bool) bool {
	for _, v := range strings.Split(tokenSubjects, ",") {
		if util.StrContains(util.SuperRoles, v) {
			return true
		}
		if requiredSubject == v {
			return true
		}
		if evalTenant(requiredSubject, v) {
			return true
		}
	}

	return false
}

// ExtractEvalTenant is a customized function to evaluate subject against tenant
func ExtractEvalTenant(requiredSubject, tokenSub string) bool {
	// expect - in subject unless it is superuser
	var sub string
	parts := strings.Split(tokenSub, subDelimiter)
	if len(parts) < 2 {
		sub = parts[0]
	}

	validLength := len(parts) - 1
	sub = strings.Join(parts[:validLength], subDelimiter)
	if sub != "" && requiredSubject == sub {
		return true
	}
	return false
}

// GetTopicFnFromRoute builds a valida topic fullname from the http route
func GetTopicFnFromRoute(vars map[string]string) (string, error) {
	tenant, ok := vars["tenant"]
	namespace, ok2 := vars["namespace"]
	topic, ok3 := vars["topic"]
	persistent, ok4 := vars["persistent"]
	if !(ok && ok2 && ok3 && ok4) {
		return "", fmt.Errorf("missing topic parts")
	}
	topicFn, err := util.BuildTopicFn(persistent, tenant, namespace, topic)
	if err != nil {
		return "", err
	}
	return topicFn, nil
}

// ConsumerParams returns a configuration parameters for Pulsar consumer
func ConsumerParams(params url.Values) (subName string, subInitPos pulsar.SubscriptionInitialPosition, subType pulsar.SubscriptionType, err error) {
	subType, err = model.GetSubscriptionType(util.QueryParamString(params, "SubscriptionType", "exclusive"))
	if err != nil {
		return "", -1, -1, err
	}
	subInitPos, err = model.GetInitialPosition(util.QueryParamString(params, "SubscriptionInitialPosition", "latest"))
	if err != nil {
		return "", -1, -1, err
	}

	subName = util.QueryParamString(params, "SubscriptionName", "")
	if len(subName) == 0 {
		name, err := util.NewUUID()
		if err != nil {
			return "", -1, -1, fmt.Errorf("failed to generate uuid error %v", err)
		}
		return model.NonResumable + name, subInitPos, subType, nil
	} else if len(subName) < 5 {
		return "", -1, -1, fmt.Errorf("subscription name must be more than 4 characters")
	}
	return subName, subInitPos, subType, nil
}

// ConsumerConfigFromHTTPParts returns configuration parameters required to generate Pulsar Client and Consumer
func ConsumerConfigFromHTTPParts(allowedClusters []string, h *http.Header, vars map[string]string, params url.Values) (token, topicFN, pulsarURL, subName string, subInitPos pulsar.SubscriptionInitialPosition, subType pulsar.SubscriptionType, err error) {
	token, _, pulsarURL, err = util.ReceiverHeader(allowedClusters, h)
	if err != nil {
		return "", "", "", "", -1, -1, err
	}

	topicFN, err = GetTopicFnFromRoute(vars)
	if err != nil {
		return "", "", "", "", -1, -1, err
	}

	subName, subInitPos, subType, err = ConsumerParams(params)
	if err != nil {
		return "", "", "", "", -1, -1, err
	}

	return token, topicFN, pulsarURL, subName, subInitPos, subType, nil
}
