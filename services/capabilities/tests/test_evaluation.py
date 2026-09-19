from velin_capabilities.eval_cli import run_corpus


async def test_golden_corpus_structural_expectations() -> None:
    result = await run_corpus()
    assert result["gate_passed"] is True
    assert result["deterministic"]["total"] == 11
    assert result["model_based"]["executed"] is False


async def test_worse_configuration_is_rejected() -> None:
    result = await run_corpus(regressed=True)
    assert result["gate_passed"] is False
    failed = [case for case in result["deterministic"]["cases"] if not case["passed"]]
    assert len(failed) == 7
    assert all(case["actual"] == "requires_human" for case in failed)
