package lds

import (
	xds_core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	xds_listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/golang/protobuf/ptypes"
	"github.com/golang/protobuf/ptypes/any"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/openservicemesh/osm/pkg/configurator"
	"github.com/openservicemesh/osm/pkg/envoy"
	"github.com/openservicemesh/osm/pkg/envoy/route"
	"github.com/openservicemesh/osm/pkg/service"
)

const (
	inboundMeshFilterChainName = "inbound-mesh-filter-chain"
)

var (
	// supportedDownstreamHTTPProtocols is the list of allowed HTTP protocols that the
	// downstream can use in an HTTP request. Since the downstream client is only allowed
	// to send plaintext traffic to an in-mesh destinations, we do not include HTTP2 over
	// TLS (h2) in this list.
	supportedDownstreamHTTPProtocols = []string{"http/1.0", "http/1.1", "h2c"}
)

func getInboundInMeshFilterChain(proxyServiceName service.MeshService, cfg configurator.Configurator) (*xds_listener.FilterChain, error) {
	marshalledDownstreamTLSContext, err := ptypes.MarshalAny(envoy.GetDownstreamTLSContext(proxyServiceName, true /* mTLS */))
	if err != nil {
		log.Error().Err(err).Msgf("Error marshalling DownstreamTLSContext object for proxy %s", proxyServiceName)
		return nil, err
	}

	inboundConnManager := getHTTPConnectionManager(route.InboundRouteConfigName, cfg)
	marshalledInboundConnManager, err := ptypes.MarshalAny(inboundConnManager)
	if err != nil {
		log.Error().Err(err).Msgf("Error marshalling inbound HttpConnectionManager object for proxy %s", proxyServiceName)
		return nil, err
	}

	filterChain := &xds_listener.FilterChain{
		Name: inboundMeshFilterChainName,
		Filters: []*xds_listener.Filter{
			{
				Name: wellknown.HTTPConnectionManager,
				ConfigType: &xds_listener.Filter_TypedConfig{
					TypedConfig: marshalledInboundConnManager,
				},
			},
		},

		// The 'FilterChainMatch' field defines the criteria for matching traffic against filters in this filter chain
		FilterChainMatch: &xds_listener.FilterChainMatch{
			// The ServerName is the SNI set by the downstream in the UptreamTlsContext by GetUpstreamTLSContext()
			// This is not a field obtained from the mTLS Certificate.
			ServerNames: []string{proxyServiceName.ServerName()},

			// Only match when transport protocol is TLS
			TransportProtocol: envoy.TransportProtocolTLS,

			// In-mesh proxies will advertise this, set in the UpstreamTlsContext by GetUpstreamTLSContext()
			ApplicationProtocols: envoy.ALPNInMesh,
		},

		TransportSocket: &xds_core.TransportSocket{
			Name: wellknown.TransportSocketTls,
			ConfigType: &xds_core.TransportSocket_TypedConfig{
				TypedConfig: marshalledDownstreamTLSContext,
			},
		},
	}

	return filterChain, nil
}

// getOutboundHTTPFilter returns an HTTP connection manager network filter used to filter outbound HTTP traffic
func (lb *listenerBuilder) getOutboundHTTPFilter() (*xds_listener.Filter, error) {
	var marshalledFilter *any.Any
	var err error

	marshalledFilter, err = ptypes.MarshalAny(
		getHTTPConnectionManager(route.OutboundRouteConfigName, lb.cfg))
	if err != nil {
		log.Error().Err(err).Msgf("Error marshalling HTTP connection manager object")
		return nil, err
	}

	return &xds_listener.Filter{
		Name:       wellknown.HTTPConnectionManager,
		ConfigType: &xds_listener.Filter_TypedConfig{TypedConfig: marshalledFilter},
	}, nil
}

// getOutboundHTTPFilterChainMatchForService builds a filter chain to match the HTTP baseddestination traffic.
// Filter Chain currently matches on the following:
// 1. Destination IP of service endpoints
// 2. HTTP application protocols
func (lb *listenerBuilder) getOutboundHTTPFilterChainMatchForService(dstSvc service.MeshService) (*xds_listener.FilterChainMatch, error) {
	filterMatch := &xds_listener.FilterChainMatch{
		// HTTP filter chain should only match on supported HTTP protocols that the downstream can use
		// to originate a request.
		ApplicationProtocols: supportedDownstreamHTTPProtocols,
	}

	endpoints, err := lb.meshCatalog.GetResolvableServiceEndpoints(dstSvc)
	if err != nil {
		log.Error().Err(err).Msgf("Error getting GetResolvableServiceEndpoints for %s", dstSvc.String())
		return nil, err
	}

	if len(endpoints) == 0 {
		log.Info().Msgf("No resolvable endpoints retured for service %s", dstSvc.String())
		return nil, nil
	}

	for _, endp := range endpoints {
		filterMatch.PrefixRanges = append(filterMatch.PrefixRanges, &xds_core.CidrRange{
			AddressPrefix: endp.IP.String(),
			PrefixLen: &wrapperspb.UInt32Value{
				Value: singleIpv4Mask,
			},
		})
	}

	return filterMatch, nil
}
