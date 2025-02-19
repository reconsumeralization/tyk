# Generated by the gRPC Python protocol compiler plugin. DO NOT EDIT!
"""Client and server classes corresponding to protobuf-defined services."""
import grpc
import warnings

import coprocess_object_pb2 as coprocess__object__pb2

GRPC_GENERATED_VERSION = '1.64.1'
GRPC_VERSION = grpc.__version__
EXPECTED_ERROR_RELEASE = '1.65.0'
SCHEDULED_RELEASE_DATE = 'June 25, 2024'
_version_not_supported = False

try:
    from grpc._utilities import first_version_is_lower
    _version_not_supported = first_version_is_lower(GRPC_VERSION, GRPC_GENERATED_VERSION)
except ImportError:
    _version_not_supported = True

if _version_not_supported:
    warnings.warn(
        f'The grpc package installed is at version {GRPC_VERSION},'
        + f' but the generated code in coprocess_object_pb2_grpc.py depends on'
        + f' grpcio>={GRPC_GENERATED_VERSION}.'
        + f' Please upgrade your grpc module to grpcio>={GRPC_GENERATED_VERSION}'
        + f' or downgrade your generated code using grpcio-tools<={GRPC_VERSION}.'
        + f' This warning will become an error in {EXPECTED_ERROR_RELEASE},'
        + f' scheduled for release on {SCHEDULED_RELEASE_DATE}.',
        RuntimeWarning
    )


class DispatcherStub(object):
    """Dispatcher is the service interface that must be implemented by the target language.
    """

    def __init__(self, channel):
        """Constructor.

        Args:
            channel: A grpc.Channel.
        """
        self.Dispatch = channel.unary_unary(
                '/coprocess.Dispatcher/Dispatch',
                request_serializer=coprocess__object__pb2.Object.SerializeToString,
                response_deserializer=coprocess__object__pb2.Object.FromString,
                _registered_method=True)
        self.DispatchEvent = channel.unary_unary(
                '/coprocess.Dispatcher/DispatchEvent',
                request_serializer=coprocess__object__pb2.Event.SerializeToString,
                response_deserializer=coprocess__object__pb2.EventReply.FromString,
                _registered_method=True)


class DispatcherServicer(object):
    """Dispatcher is the service interface that must be implemented by the target language.
    """

    def Dispatch(self, request, context):
        """Dispatch is an RPC method that accepts and returns an Object.
        """
        context.set_code(grpc.StatusCode.UNIMPLEMENTED)
        context.set_details('Method not implemented!')
        raise NotImplementedError('Method not implemented!')

    def DispatchEvent(self, request, context):
        """DispatchEvent dispatches an event to the target language.
        """
        context.set_code(grpc.StatusCode.UNIMPLEMENTED)
        context.set_details('Method not implemented!')
        raise NotImplementedError('Method not implemented!')


def add_DispatcherServicer_to_server(servicer, server):
    rpc_method_handlers = {
            'Dispatch': grpc.unary_unary_rpc_method_handler(
                    servicer.Dispatch,
                    request_deserializer=coprocess__object__pb2.Object.FromString,
                    response_serializer=coprocess__object__pb2.Object.SerializeToString,
            ),
            'DispatchEvent': grpc.unary_unary_rpc_method_handler(
                    servicer.DispatchEvent,
                    request_deserializer=coprocess__object__pb2.Event.FromString,
                    response_serializer=coprocess__object__pb2.EventReply.SerializeToString,
            ),
    }
    generic_handler = grpc.method_handlers_generic_handler(
            'coprocess.Dispatcher', rpc_method_handlers)
    server.add_generic_rpc_handlers((generic_handler,))
    server.add_registered_method_handlers('coprocess.Dispatcher', rpc_method_handlers)


 # This class is part of an EXPERIMENTAL API.
class Dispatcher(object):
    """Dispatcher is the service interface that must be implemented by the target language.
    """

    @staticmethod
    def Dispatch(request,
            target,
            options=(),
            channel_credentials=None,
            call_credentials=None,
            insecure=False,
            compression=None,
            wait_for_ready=None,
            timeout=None,
            metadata=None):
        return grpc.experimental.unary_unary(
            request,
            target,
            '/coprocess.Dispatcher/Dispatch',
            coprocess__object__pb2.Object.SerializeToString,
            coprocess__object__pb2.Object.FromString,
            options,
            channel_credentials,
            insecure,
            call_credentials,
            compression,
            wait_for_ready,
            timeout,
            metadata,
            _registered_method=True)

    @staticmethod
    def DispatchEvent(request,
            target,
            options=(),
            channel_credentials=None,
            call_credentials=None,
            insecure=False,
            compression=None,
            wait_for_ready=None,
            timeout=None,
            metadata=None):
        return grpc.experimental.unary_unary(
            request,
            target,
            '/coprocess.Dispatcher/DispatchEvent',
            coprocess__object__pb2.Event.SerializeToString,
            coprocess__object__pb2.EventReply.FromString,
            options,
            channel_credentials,
            insecure,
            call_credentials,
            compression,
            wait_for_ready,
            timeout,
            metadata,
            _registered_method=True)
