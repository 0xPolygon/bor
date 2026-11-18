// This test calls a method that returns a nil pointer to a type
// with a value-receiver MarshalJSON. The result must be null.

--> {"jsonrpc": "2.0", "id": 1, "method": "test_nilMarshaler", "params": []}
<-- {"jsonrpc":"2.0","id":1,"result":null}
