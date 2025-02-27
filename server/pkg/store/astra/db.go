package astra

import (
	"fmt"
	"strings"

	astraclient "github.com/kube-tarian/kad/server/pkg/astra-client"
	"github.com/kube-tarian/kad/server/pkg/types"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stargate/stargate-grpc-go-client/stargate/pkg/client"
	pb "github.com/stargate/stargate-grpc-go-client/stargate/pkg/proto"
)

type AstraServerStore struct {
	c        *astraclient.Client
	keyspace string
}

func NewStore() (*AstraServerStore, error) {
	a := &AstraServerStore{}
	err := a.initClient()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to astra db, %w", err)
	}
	a.keyspace = a.c.Keyspace()
	return a, nil
}

func (a *AstraServerStore) initClient() error {
	var err error
	a.c, err = astraclient.NewClient()
	return err
}

func (a *AstraServerStore) InitializeDb() error {
	initDbQueries := []string{
		fmt.Sprintf(createClusterEndpointTableQuery, a.keyspace),
		fmt.Sprintf(createOrgClusterTableQuery, a.keyspace),
	}

	for _, query := range initDbQueries {
		createQuery := &pb.Query{
			Cql: query,
		}

		_, err := a.c.Session().ExecuteQuery(createQuery)
		if err != nil {
			return fmt.Errorf("failed to initialise db: %w", err)
		}
	}

	return nil
}

func (a *AstraServerStore) GetClusterEndpoint(orgID, clusterName string) (string, error) {
	clusterId, err := a.getClusterID(orgID, clusterName)
	if err != nil {
		return "", fmt.Errorf("failed to get cluster endpoint: %w", err)
	}

	endpointQuery := &pb.Query{
		Cql: fmt.Sprintf("Select endpoint FROM %s.cluster_endpoint WHERE cluster_id=%s;",
			a.keyspace, clusterId),
	}

	response, err := a.c.Session().ExecuteQuery(endpointQuery)
	if err != nil {
		return "", err
	}
	result := response.GetResultSet()

	if len(result.Rows) == 0 {
		return "", fmt.Errorf("cluster: %s not found", clusterName)
	}

	endpoint, err := client.ToString(result.Rows[0].Values[0])
	if err != nil {
		return "", fmt.Errorf("cluster: %s unable to convert endpoint to string", clusterName)
	}

	return endpoint, nil
}

func (a *AstraServerStore) AddCluster(orgId, clusterName, endpoint string) error {
	clusterExists, err := a.clusterEntryExists(orgId)
	if err != nil {
		return fmt.Errorf("failed to store cluster details: %w", err)
	}

	clusterId := gocql.TimeUUID()
	var batchQueries []*pb.BatchQuery
	if clusterExists {
		batchQueries = append(batchQueries,
			&pb.BatchQuery{
				Cql: fmt.Sprintf(
					"UPDATE %s.org_cluster SET cluster_ids= cluster_ids + {%s} WHERE org_id=%s;",
					a.keyspace, clusterId.String(), orgId),
			})
	} else {
		batchQueries = append(batchQueries,
			&pb.BatchQuery{
				Cql: fmt.Sprintf("INSERT INTO %s.org_cluster(org_id, cluster_ids) VALUES (%s, {%s});",
					a.keyspace, orgId, clusterId),
			})
	}

	batchQueries = append(batchQueries,
		&pb.BatchQuery{
			Cql: fmt.Sprintf("INSERT INTO %s.cluster_endpoint (cluster_id, org_id, cluster_name, endpoint) VALUES (%s, %s, '%s', '%s');",
				a.keyspace, clusterId, orgId, clusterName, endpoint),
		})

	batch := &pb.Batch{
		Type:    pb.Batch_LOGGED,
		Queries: batchQueries,
	}

	_, err = a.c.Session().ExecuteBatch(batch)
	if err != nil {
		return fmt.Errorf("failed store cluster details %w", err)
	}

	return nil
}

func (a *AstraServerStore) clusterEntryExists(orgID string) (bool, error) {
	selectClusterQuery := &pb.Query{
		Cql: fmt.Sprintf("Select cluster_ids FROM %s.org_cluster WHERE org_id=%s ;",
			a.keyspace, orgID),
	}

	response, err := a.c.Session().ExecuteQuery(selectClusterQuery)
	if err != nil {
		return false, fmt.Errorf("failed to initialise db: %w", err)
	}

	result := response.GetResultSet()
	if len(result.Rows) > 0 {
		return true, nil
	}

	return false, nil
}

func (a *AstraServerStore) UpdateCluster(orgID, clusterName, endpoint string) error {
	clusterId, err := a.getClusterID(orgID, clusterName)
	if err != nil {
		return fmt.Errorf("failed to update the cluster info: %w", err)
	}

	updateQuery := &pb.Query{
		Cql: fmt.Sprintf(
			"UPDATE %s.cluster_endpoint set endpoint='%s' WHERE cluster_id=%s AND org_id=%s",
			a.keyspace, endpoint, clusterId, orgID),
	}

	_, err = a.c.Session().ExecuteQuery(updateQuery)
	if err != nil {
		return fmt.Errorf("failed to update cluster info: %w", err)
	}

	return nil
}

func (a *AstraServerStore) DeleteCluster(orgID, clusterName string) error {
	clusterId, err := a.getClusterID(orgID, clusterName)
	if err != nil {
		return fmt.Errorf("failed to delete cluster info: %w", err)
	}

	batch := &pb.Batch{
		Type: pb.Batch_LOGGED,
		Queries: []*pb.BatchQuery{
			{
				Cql: fmt.Sprintf(
					"DELETE FROM %s.cluster_endpoint WHERE cluster_id=%s ;",
					a.keyspace, clusterId),
			},
			{
				Cql: fmt.Sprintf(
					"UPDATE %s.org_cluster set cluster_ids = cluster_ids - {%s} WHERE org_id=%s ;",
					a.keyspace, clusterId, orgID),
			},
		},
	}

	_, err = a.c.Session().ExecuteBatch(batch)
	if err != nil {
		return fmt.Errorf("failed delete cluster details %w", err)
	}

	return nil
}

func (a *AstraServerStore) GetClusters(orgID string) ([]types.ClusterDetails, error) {
	selectClusterIdsQuery := &pb.Query{
		Cql: fmt.Sprintf("Select cluster_ids FROM %s.org_cluster WHERE org_id=%s ;",
			a.keyspace, orgID),
	}

	response, err := a.c.Session().ExecuteQuery(selectClusterIdsQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster info from db: %w", err)
	}

	result := response.GetResultSet()
	if len(result.Rows) == 0 {
		return []types.ClusterDetails{}, nil
	}

	clusterIds, err := client.ToSet(result.Rows[0].Values[0], UuidSetSpec)
	if err != nil {
		return nil, err
	}

	var clusterIdStrs []string
	for _, clusterId := range clusterIds.([]interface{}) {
		clusterIdStrs = append(clusterIdStrs, clusterId.(*uuid.UUID).String())
	}

	selectClustersQuery := &pb.Query{
		Cql: fmt.Sprintf("Select cluster_name, endpoint FROM %s.cluster_endpoint WHERE cluster_id in (%s);",
			a.keyspace, strings.Join(clusterIdStrs, ",")),
	}

	response, err = a.c.Session().ExecuteQuery(selectClustersQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster endpoint info from db: %w", err)
	}

	result = response.GetResultSet()
	var clusterDetails []types.ClusterDetails
	for _, row := range result.Rows {
		cqlClusterName, err := client.ToString(row.Values[0])
		if err != nil {
			return nil, fmt.Errorf("failed to get cluster name: %w", err)
		}

		cqlEndpoint, err := client.ToString(row.Values[1])
		if err != nil {
			return nil, fmt.Errorf("failed to get cluster endpoint: %w", err)
		}

		clusterDetails = append(clusterDetails,
			types.ClusterDetails{
				ClusterName: cqlClusterName,
				Endpoint:    cqlEndpoint,
			})
	}

	return clusterDetails, nil
}

func (a *AstraServerStore) getClusterID(orgID, clusterName string) (string, error) {
	selectClusterIdsQuery := &pb.Query{
		Cql: fmt.Sprintf("Select cluster_ids FROM %s.org_cluster WHERE org_id=%s ;",
			a.keyspace, orgID),
	}

	response, err := a.c.Session().ExecuteQuery(selectClusterIdsQuery)
	if err != nil {
		return "", fmt.Errorf("failed to get cluster info from db: %w", err)
	}

	result := response.GetResultSet()
	if len(result.Rows) == 0 {
		return "", fmt.Errorf("no cluster found for orgId %s", orgID)
	}

	clusterIds, err := client.ToSet(result.Rows[0].Values[0], UuidSetSpec)
	if err != nil {
		return "", err
	}

	var clusterIdStrs []string
	for _, clusterId := range clusterIds.([]interface{}) {
		clusterIdStrs = append(clusterIdStrs, clusterId.(*uuid.UUID).String())
	}

	selectClustersQuery := &pb.Query{
		Cql: fmt.Sprintf("Select cluster_id, cluster_name FROM %s.cluster_endpoint WHERE cluster_id in (%s);",
			a.keyspace, strings.Join(clusterIdStrs, ",")),
	}

	response, err = a.c.Session().ExecuteQuery(selectClustersQuery)
	if err != nil {
		return "", fmt.Errorf("failed to get cluster endpoint info from db: %w", err)
	}

	result = response.GetResultSet()
	for _, row := range result.Rows {
		cqlClusterId, err := client.ToUUID(row.Values[0])
		if err != nil {
			return "", fmt.Errorf("failed to get cluster uuid: %w", err)
		}

		cqlClusterName, err := client.ToString(row.Values[1])
		if err != nil {
			return "", fmt.Errorf("failed to get cluster name: %w", err)
		}

		if cqlClusterName == clusterName {
			return cqlClusterId.String(), nil
		}
	}

	return "", fmt.Errorf("cluster not found")
}
