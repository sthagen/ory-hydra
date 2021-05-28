// Code generated by go-swagger; DO NOT EDIT.

package admin

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	"fmt"
	"io"

	"github.com/go-openapi/runtime"
	"github.com/go-openapi/strfmt"

	"github.com/ory/hydra/internal/httpclient/models"
)

// CreateJwtBearerGrantReader is a Reader for the CreateJwtBearerGrant structure.
type CreateJwtBearerGrantReader struct {
	formats strfmt.Registry
}

// ReadResponse reads a server response into the received o.
func (o *CreateJwtBearerGrantReader) ReadResponse(response runtime.ClientResponse, consumer runtime.Consumer) (interface{}, error) {
	switch response.Code() {
	case 201:
		result := NewCreateJwtBearerGrantCreated()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return result, nil
	case 400:
		result := NewCreateJwtBearerGrantBadRequest()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return nil, result
	case 409:
		result := NewCreateJwtBearerGrantConflict()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return nil, result
	case 500:
		result := NewCreateJwtBearerGrantInternalServerError()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return nil, result
	default:
		return nil, runtime.NewAPIError("response status code does not match any response statuses defined for this endpoint in the swagger spec", response, response.Code())
	}
}

// NewCreateJwtBearerGrantCreated creates a CreateJwtBearerGrantCreated with default headers values
func NewCreateJwtBearerGrantCreated() *CreateJwtBearerGrantCreated {
	return &CreateJwtBearerGrantCreated{}
}

/* CreateJwtBearerGrantCreated describes a response with status code 201, with default header values.

JwtBearerGrant
*/
type CreateJwtBearerGrantCreated struct {
	Payload *models.JwtBearerGrant
}

func (o *CreateJwtBearerGrantCreated) Error() string {
	return fmt.Sprintf("[POST /grants/jwt-bearer][%d] createJwtBearerGrantCreated  %+v", 201, o.Payload)
}
func (o *CreateJwtBearerGrantCreated) GetPayload() *models.JwtBearerGrant {
	return o.Payload
}

func (o *CreateJwtBearerGrantCreated) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.JwtBearerGrant)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewCreateJwtBearerGrantBadRequest creates a CreateJwtBearerGrantBadRequest with default headers values
func NewCreateJwtBearerGrantBadRequest() *CreateJwtBearerGrantBadRequest {
	return &CreateJwtBearerGrantBadRequest{}
}

/* CreateJwtBearerGrantBadRequest describes a response with status code 400, with default header values.

genericError
*/
type CreateJwtBearerGrantBadRequest struct {
	Payload *models.GenericError
}

func (o *CreateJwtBearerGrantBadRequest) Error() string {
	return fmt.Sprintf("[POST /grants/jwt-bearer][%d] createJwtBearerGrantBadRequest  %+v", 400, o.Payload)
}
func (o *CreateJwtBearerGrantBadRequest) GetPayload() *models.GenericError {
	return o.Payload
}

func (o *CreateJwtBearerGrantBadRequest) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.GenericError)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewCreateJwtBearerGrantConflict creates a CreateJwtBearerGrantConflict with default headers values
func NewCreateJwtBearerGrantConflict() *CreateJwtBearerGrantConflict {
	return &CreateJwtBearerGrantConflict{}
}

/* CreateJwtBearerGrantConflict describes a response with status code 409, with default header values.

genericError
*/
type CreateJwtBearerGrantConflict struct {
	Payload *models.GenericError
}

func (o *CreateJwtBearerGrantConflict) Error() string {
	return fmt.Sprintf("[POST /grants/jwt-bearer][%d] createJwtBearerGrantConflict  %+v", 409, o.Payload)
}
func (o *CreateJwtBearerGrantConflict) GetPayload() *models.GenericError {
	return o.Payload
}

func (o *CreateJwtBearerGrantConflict) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.GenericError)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewCreateJwtBearerGrantInternalServerError creates a CreateJwtBearerGrantInternalServerError with default headers values
func NewCreateJwtBearerGrantInternalServerError() *CreateJwtBearerGrantInternalServerError {
	return &CreateJwtBearerGrantInternalServerError{}
}

/* CreateJwtBearerGrantInternalServerError describes a response with status code 500, with default header values.

genericError
*/
type CreateJwtBearerGrantInternalServerError struct {
	Payload *models.GenericError
}

func (o *CreateJwtBearerGrantInternalServerError) Error() string {
	return fmt.Sprintf("[POST /grants/jwt-bearer][%d] createJwtBearerGrantInternalServerError  %+v", 500, o.Payload)
}
func (o *CreateJwtBearerGrantInternalServerError) GetPayload() *models.GenericError {
	return o.Payload
}

func (o *CreateJwtBearerGrantInternalServerError) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.GenericError)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}