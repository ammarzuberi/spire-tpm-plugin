/*
 ** Copyright 2019 Bloomberg Finance L.P.
 **
 ** Licensed under the Apache License, Version 2.0 (the "License");
 ** you may not use this file except in compliance with the License.
 ** You may obtain a copy of the License at
 **
 **     http://www.apache.org/licenses/LICENSE-2.0
 **
 ** Unless required by applicable law or agreed to in writing, software
 ** distributed under the License is distributed on an "AS IS" BASIS,
 ** WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 ** See the License for the specific language governing permissions and
 ** limitations under the License.
 */

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/google/go-attestation/attest"
	nodeattestorv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/plugin/agent/nodeattestor/v1"
	configv1 "github.com/spiffe/spire-plugin-sdk/proto/spire/service/common/config/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hashicorp/hcl"

	"github.com/bloomberg/spire-tpm-plugin/pkg/common"
)

// Plugin implements the nodeattestor Plugin interface
type Plugin struct {
	nodeattestorv1.UnsafeNodeAttestorServer
	configv1.UnsafeConfigServer
	config *Config
	tpm    *attest.TPM
	m      sync.Mutex
}

type Config struct {
	trustDomain string
}

func (p *Plugin) Configure(ctx context.Context, req *configv1.ConfigureRequest) (*configv1.ConfigureResponse, error) {
	config := &Config{}
	if err := hcl.Decode(config, req.HclConfiguration); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to decode configuration file: %v", err)
	}

	if req.CoreConfiguration == nil {
		return nil, status.Errorf(codes.InvalidArgument, "global configuration is required")
	}
	if req.CoreConfiguration.TrustDomain == "" {
		return nil, status.Errorf(codes.InvalidArgument, "trust_domain is required")
	}

	p.m.Lock()
	defer p.m.Unlock()

	config.trustDomain = req.CoreConfiguration.TrustDomain
	p.config = config

	return &configv1.ConfigureResponse{}, nil
}

func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) AidAttestation(stream nodeattestorv1.NodeAttestor_AidAttestationServer) error {
	conf := p.getConfig()
	if conf == nil {
		return status.Error(codes.FailedPrecondition, "plugin not configured")
	}

	attestationData, aik, err := p.generateAttestationData()
	if err != nil {
		return status.Errorf(codes.Internal, "failed to generate attestation data: %v", err)
	}

	attestationDataBytes, err := json.Marshal(attestationData)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to marshal attestation data to json: %v", err)
	}

	err = stream.Send(&nodeattestorv1.PayloadOrChallengeResponse{
		Data: &nodeattestorv1.PayloadOrChallengeResponse_Payload{
			Payload: attestationDataBytes,
		},
	})

	if err != nil {
		return status.Errorf(status.Code(err), "failed to send attestation data: %v", err)
	}

	resp, err := stream.Recv()
	if err != nil {
		return status.Errorf(status.Code(err), "failed to receive challenge: %v", err)
	}

	challenge := new(common.Challenge)
	if err := json.Unmarshal(resp.Challenge, challenge); err != nil {
		return status.Errorf(codes.InvalidArgument, "failed to unmarshal challenge: %v", err)
	}

	response, err := p.calculateResponse(challenge.EC, aik)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "failed to calculate response: %v", err)
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "unable to marshal challenge response: %v", err)
	}

	err = stream.Send(&nodeattestorv1.PayloadOrChallengeResponse{
		Data: &nodeattestorv1.PayloadOrChallengeResponse_ChallengeResponse{
			ChallengeResponse: responseBytes,
		},
	})

	if err != nil {
		return status.Errorf(status.Code(err), "unable to send challenge response: %v", err)
	}

	return nil
}

func (p *Plugin) calculateResponse(ec *attest.EncryptedCredential, aikBytes []byte) (*common.ChallengeResponse, error) {
	tpm := p.tpm
	if tpm == nil {
		var err error
		tpm, err = attest.OpenTPM(&attest.OpenConfig{
			TPMVersion: attest.TPMVersion20,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to connect to tpm: %v", err)
		}
		defer tpm.Close()
	}

	aik, err := tpm.LoadAK(aikBytes)
	if err != nil {
		return nil, err
	}
	defer aik.Close(tpm)

	secret, err := aik.ActivateCredential(tpm, *ec)
	if err != nil {
		return nil, fmt.Errorf("failed to activate credential: %v", err)
	}
	return &common.ChallengeResponse{
		Secret: secret,
	}, nil
}

func (p *Plugin) generateAttestationData() (*common.AttestationData, []byte, error) {
	tpm := p.tpm
	if tpm == nil {
		var err error
		tpm, err = attest.OpenTPM(&attest.OpenConfig{
			TPMVersion: attest.TPMVersion20,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to connect to tpm: %v", err)
		}
		defer tpm.Close()
	}

	eks, err := tpm.EKs()
	if err != nil {
		return nil, nil, err
	}
	ak, err := tpm.NewAK(nil)
	if err != nil {
		return nil, nil, err
	}
	defer ak.Close(tpm)
	params := ak.AttestationParameters()

	if len(eks) == 0 {
		return nil, nil, errors.New("no EK available")
	}

	ek := &eks[0]
	ekBytes, err := common.EncodeEK(ek)
	if err != nil {
		return nil, nil, err
	}

	aikBytes, err := ak.Marshal()
	if err != nil {
		return nil, nil, err
	}

	return &common.AttestationData{
		EK: ekBytes,
		AK: &params,
	}, aikBytes, nil
}

func (p *Plugin) getConfig() *Config {
	p.m.Lock()
	defer p.m.Unlock()
	return p.config
}
