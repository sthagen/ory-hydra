package cmd_test

import (
	"context"
	"encoding/base64"
	"io/ioutil"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/ory/hydra/client"
	"github.com/ory/hydra/cmd/cliclient"
	"github.com/ory/hydra/driver"
	"github.com/ory/hydra/internal"
	"github.com/ory/hydra/internal/testhelpers"
	"github.com/ory/x/cmdx"
	"github.com/ory/x/contextx"
	"github.com/ory/x/snapshotx"
)

func base64EncodedPGPPublicKey(t *testing.T) string {
	t.Helper()
	gpgPublicKey, err := ioutil.ReadFile("../test/stub/pgp.pub")
	if err != nil {
		t.Fatal(err)
	}

	return base64.StdEncoding.EncodeToString(gpgPublicKey)
}

func setup(t *testing.T, cmd *cobra.Command) driver.Registry {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reg := internal.NewMockedRegistry(t, &contextx.Default{})
	_, admin := testhelpers.NewOAuth2Server(ctx, t, reg)

	cliclient.RegisterClientFlags(cmd.Flags())
	cmdx.RegisterFormatFlags(cmd.Flags())
	require.NoError(t, cmd.Flags().Set(cliclient.FlagEndpoint, admin.URL))
	require.NoError(t, cmd.Flags().Set(cmdx.FlagFormat, string(cmdx.FormatJSON)))
	return reg
}

var snapshotExcludedClientFields = []snapshotx.ExceptOpt{
	snapshotx.ExceptNestedKeys("client_id"),
	snapshotx.ExceptNestedKeys("registration_access_token"),
	snapshotx.ExceptNestedKeys("registration_client_uri"),
	snapshotx.ExceptNestedKeys("client_secret"),
	snapshotx.ExceptNestedKeys("created_at"),
	snapshotx.ExceptNestedKeys("updated_at"),
}

func createClient(t *testing.T, reg driver.Registry, c *client.Client) *client.Client {
	if c == nil {
		c = &client.Client{}
	}
	require.NoError(t, reg.ClientManager().CreateClient(context.Background(), c))
	return c
}