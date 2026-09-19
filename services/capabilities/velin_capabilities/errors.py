from velin_capabilities.schemas import Invocation, ToolCall


class CapabilityError(Exception):
    def __init__(
        self,
        code: str,
        message: str,
        status: int = 503,
        *,
        tool_calls: list[ToolCall] | None = None,
        invocations: list[Invocation] | None = None,
    ) -> None:
        super().__init__(message)
        self.code = code
        self.message = message
        self.status = status
        self.tool_calls = (tool_calls or [])[-16:]
        self.invocations = (invocations or [])[-8:]


class ProviderError(CapabilityError):
    def __init__(self, code: str, retryable: bool) -> None:
        super().__init__(
            code, "Model provider could not complete the request.", 503 if retryable else 422
        )
        self.retryable = retryable
