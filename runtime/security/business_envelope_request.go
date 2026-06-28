package security

const (
	BusinessEnvelopeStatusOK           = 0
	BusinessEnvelopeStatusBadRequest   = 400
	BusinessEnvelopeStatusUnauthorized = 401
	BusinessEnvelopeStatusConflict     = 409
)

type BusinessEnvelopeRequest struct {
	SessionID   string
	Version     string
	Seq         int64
	TimestampMS int64
	Nonce       string
	Op          string
	KeyID       string
	Tag         string
	Mode        string
	Transport   string
	Endpoint    string
	UserID      string
	BodyHash    string
}

type BusinessEnvelopeRequestResult struct {
	OK      bool
	Status  int
	Code    string
	Reason  string
	Message string
	Replay  bool
	Seq     int64
	Nonce   string
}

func ValidateBusinessEnvelopeRequest(guard *BusinessEnvelopeGuard, request BusinessEnvelopeRequest) BusinessEnvelopeRequestResult {
	if guard == nil {
		guard = NewBusinessEnvelopeGuard()
	}
	validation := guard.Validate(BusinessEnvelope{
		SessionID:   request.SessionID,
		Version:     request.Version,
		Seq:         request.Seq,
		TimestampMS: request.TimestampMS,
		Nonce:       request.Nonce,
		Op:          request.Op,
		KeyID:       request.KeyID,
		Tag:         request.Tag,
		Mode:        request.Mode,
		Transport:   request.Transport,
		Endpoint:    request.Endpoint,
		UserID:      request.UserID,
		BodyHash:    request.BodyHash,
	})
	if validation.OK {
		return BusinessEnvelopeRequestResult{
			OK:     true,
			Status: BusinessEnvelopeStatusOK,
			Seq:    validation.Seq,
			Nonce:  validation.Nonce,
		}
	}
	return BusinessEnvelopeRequestResult{
		OK:      false,
		Status:  businessEnvelopeStatusFor(validation),
		Code:    validation.Code,
		Reason:  validation.Reason,
		Message: validation.Message,
		Replay:  validation.Replay,
		Seq:     validation.Seq,
		Nonce:   validation.Nonce,
	}
}

func businessEnvelopeStatusFor(validation BusinessEnvelopeValidation) int {
	if validation.Reason == ReasonSessionMissing {
		return BusinessEnvelopeStatusUnauthorized
	}
	if validation.Code == CodeBusinessEnvelopeReplay {
		return BusinessEnvelopeStatusConflict
	}
	return BusinessEnvelopeStatusBadRequest
}
