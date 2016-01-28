package parser

import (
	"fmt"
	"os"
	"reflect"

	"github.com/ark-lang/ark/src/lexer"
	"github.com/ark-lang/ark/src/util"
	"github.com/ark-lang/ark/src/util/log"
)

type TypeVariable struct {
	metaType
	Id int
}

func (v *TypeVariable) Equals(other Type) bool {
	if ot, ok := other.(*TypeVariable); ok {
		return v.Id == ot.Id
	}
	return false
}

func (v *TypeVariable) String() string {
	return NewASTStringer("TypeVariable").AddType(v).Finish()
}

func (v *TypeVariable) TypeName() string {
	return fmt.Sprintf("$%d", v.Id)
}

func (v *TypeVariable) ActualType() Type {
	return v
}

type ConstructorId int

const (
	ConstructorInvalid ConstructorId = iota
	ConstructorStructMember

	// TODO: This guy goes away once we remove tuple indexing and replace with
	// tuple destructuring
	ConstructorTupleIndex
)

type ConstructorType struct {
	metaType
	Id   ConstructorId
	Args []Type

	// Some constructors need additional data
	Data interface{}
}

func (v *ConstructorType) Equals(other Type) bool {
	if ot, ok := other.(*ConstructorType); ok {
		if v.Id != ot.Id {
			return false
		}

		if v.Data != ot.Data {
			return false
		}

		if len(v.Args) != len(ot.Args) {
			return false
		}

		for idx, arg := range v.Args {
			oarg := ot.Args[idx]
			if !arg.Equals(oarg) {
				return false
			}
		}

		return true
	}
	return false
}

func (v *ConstructorType) String() string {
	return NewASTStringer("ConstructorType").AddType(v).Finish()
}

func (v *ConstructorType) TypeName() string {
	return fmt.Sprintf("C%d(%v).%v", v.Id, v.Args, v.Data)
}

func (v *ConstructorType) ActualType() Type {
	return v
}

// Constraint represents a single constraint to be solved.
// It consists of two "sides", each representing a type or a type variable.
type Constraint struct {
	Left, Right Side
}

func ConstraintFromTypes(left Type, right Type) *Constraint {
	return &Constraint{
		Left:  SideFromType(left),
		Right: SideFromType(right),
	}
}

func (v *Constraint) String() string {
	return fmt.Sprintf("%s = %s", v.Left, v.Right)
}

func (v *Constraint) Subs(id int, side Side) *Constraint {
	res := &Constraint{
		Left:  v.Left.Subs(id, side),
		Right: v.Right.Subs(id, side),
	}
	return res
}

type SideType int

const (
	IdentSide SideType = iota
	TypeSide
)

// Side represents a single side of a constraint.
// It represents either a type (TypeSide) or a type variable (IdentSide)
type Side struct {
	SideType SideType
	Id       int
	Type     Type
}

// SideFromType creates a new Side from the given type.
// If the given type is a *TypeVariable an IdentSide will be created, otherwise
// a TypeSide will be created.
func SideFromType(t Type) Side {
	if tv, ok := t.(*TypeVariable); ok {
		return Side{SideType: IdentSide, Id: tv.Id}
	}
	return Side{SideType: TypeSide, Type: t}
}

// Subs descends through the given Side, and replaces all occurenes of the
// given id with the contents of the Side `what`.
func (v Side) Subs(id int, what Side) Side {
	switch v.SideType {
	// If this is an IdentSide we check if the id matches and return the
	// replacement side in case of a match.
	case IdentSide:
		if v.Id == id {
			return what
		}
		return v

	// If this is a TypeSide we create a type from the `what` side,
	// and then delegate the substitution to `SubsType`
	case TypeSide:
		var nt Type
		if what.SideType == TypeSide {
			nt = SubsType(v.Type, id, what.Type)
		} else {
			nt = SubsType(v.Type, id, &TypeVariable{Id: what.Id})
		}
		return Side{SideType: TypeSide, Type: nt}

	default:
		panic("Invalid SideType")
	}
}

// SubsType descends through a type and replaces all occurences of the given
// type variable by `what`
func SubsType(typ Type, id int, what Type) Type {
	switch t := typ.(type) {
	case *TypeVariable:
		if t.Id == id {
			return what
		}
		return t

	case *ConstructorType:
		// Descend through all arguments
		nargs := make([]Type, len(t.Args))
		for idx, arg := range t.Args {
			nargs[idx] = SubsType(arg, id, what)
		}

		// Handle special cases
		switch t.Id {
		// If we have a struct member, we check whether we can resolve the
		// actual type of the member with the information we have at the
		// current point. If we do, we return the actual type.
		case ConstructorStructMember:
			// Method check
			if typ, ok := TypeWithoutPointers(nargs[0]).(*NamedType); ok {
				fn := typ.GetMethod(t.Data.(string))
				if fn != nil {
					return fn.Type
				}
			}

			// Struct member
			typ := nargs[0]
			if pt, ok := typ.(PointerType); ok {
				typ = pt.Addressee
			}
			if st, ok := typ.ActualType().(StructType); ok {
				mem := st.GetMember(t.Data.(string))
				return mem.Type
			}

		// Likewise we check if we can resolve the actual type tuple index and
		// if we can, we return it.
		case ConstructorTupleIndex:
			if tt, ok := nargs[0].ActualType().(TupleType); ok {
				return tt.Members[t.Data.(uint64)]
			}
		}

		return &ConstructorType{Id: t.Id, Args: nargs, Data: t.Data}

	case FunctionType:
		// Descend into return type
		newRet := SubsType(t.Return, id, what)

		// Descend into parameter types
		np := make([]Type, len(t.Parameters))
		for idx, param := range t.Parameters {
			np[idx] = SubsType(param, id, what)
		}

		return FunctionType{
			attrs:      t.attrs,
			IsVariadic: t.IsVariadic,
			Parameters: np,
			Return:     newRet,
		}

	case TupleType:
		// Descend into member types
		nm := make([]Type, len(t.Members))
		for idx, mem := range t.Members {
			nm[idx] = SubsType(mem, id, what)
		}

		return tupleOf(nm...)

	case ArrayType:
		return ArrayOf(SubsType(t.MemberType, id, what))

	case PointerType:
		return PointerTo(SubsType(t.Addressee, id, what))

	case ConstantReferenceType:
		return constantReferenceTo(SubsType(t.Referrer, id, what))

	case MutableReferenceType:
		return mutableReferenceTo(SubsType(t.Referrer, id, what))

	// The following are noops at the current time. For NamedType and EnumType
	// this is only temporarily, until we finalize implementaiton of generics
	// in a solid maintainable way.
	case PrimitiveType, StructType, *NamedType, InterfaceType, EnumType:
		return t

	default:
		panic("Unhandled type in Side.Subs(): " + reflect.TypeOf(t).String())
	}
}

func (v Side) String() string {
	switch v.SideType {
	case IdentSide:
		return fmt.Sprintf("$%d", v.Id)
	case TypeSide:
		return fmt.Sprintf("type `%s`", v.Type.TypeName())
	}
	panic("Invalid side type")
}

type AnnotatedTyped struct {
	Pos   lexer.Position
	Typed Typed
	Id    int
}

type Inferrer struct {
	Submodule   *Submodule
	Functions   []*Function
	Typeds      map[int]*AnnotatedTyped
	TypedLookup map[Typed]*AnnotatedTyped
	Constraints []*Constraint
	IdCount     int
}

func (v *Inferrer) err(msg string, args ...interface{}) {
	log.Errorln("inferrer", "%s %s", util.Red("error:"), fmt.Sprintf(msg, args...))
	os.Exit(util.EXIT_FAILURE_SEMANTIC)
}

func (v *Inferrer) errPos(pos lexer.Position, msg string, args ...interface{}) {
	log.Errorln("inferrer", "%s: [%s:%d:%d] %s", util.Red("error:"),
		pos.Filename, pos.Line, pos.Char,
		fmt.Sprintf(msg, args...))
	log.Errorln("inferrer", "%s", v.Submodule.File.MarkPos(pos))
	os.Exit(util.EXIT_FAILURE_SEMANTIC)
}

func (v *Inferrer) Function() *Function {
	return v.Functions[len(v.Functions)-1]
}

func Infer(submod *Submodule) {
	if submod.inferred {
		return
	}
	submod.inferred = true

	for _, used := range submod.UseScope.UsedModules {
		for _, submod := range used.Parts {
			Infer(submod)
		}
	}

	log.Timed("inferring submodule", submod.File.Name, func() {
		inf := &Inferrer{
			Submodule:   submod,
			Typeds:      make(map[int]*AnnotatedTyped),
			TypedLookup: make(map[Typed]*AnnotatedTyped),
		}
		vis := NewASTVisitor(inf)
		vis.VisitSubmodule(submod)
		inf.Finalize()
	})

}

func (v *Inferrer) AddConstraint(c *Constraint) {
	v.Constraints = append(v.Constraints, c)
}

// AddEqualsConstraint creates a constraint that indicates that the two given
// ids are equal to one-another and add it to the list of constraints.
func (v *Inferrer) AddEqualsConstraint(a, b int) {
	c := &Constraint{
		Left:  Side{Id: a, SideType: IdentSide},
		Right: Side{Id: b, SideType: IdentSide},
	}
	v.AddConstraint(c)
}

// AddIsConstraint creates a constraing that indicates that the given id is of
// the given type and add it to the list of constraints.
func (v *Inferrer) AddIsConstraint(id int, typ Type) {
	c := &Constraint{
		Left:  Side{Id: id, SideType: IdentSide},
		Right: Side{Type: typ, SideType: TypeSide},
	}
	v.AddConstraint(c)
}

func (v *Inferrer) EnterScope() {}

func (v *Inferrer) ExitScope() {}

func (v *Inferrer) PostVisit(node *Node) {
	switch (*node).(type) {
	case *FunctionDecl, *LambdaExpr:
		idx := len(v.Functions) - 1
		v.Functions[idx] = nil
		v.Functions = v.Functions[:idx]
		return
	}
}

func (v *Inferrer) Visit(node *Node) bool {
	switch n := (*node).(type) {
	case *FunctionDecl:
		v.Functions = append(v.Functions, n.Function)
		return true

	case *LambdaExpr:
		v.Functions = append(v.Functions, n.Function)
		return true
	}

	// Switch on the type of a node. If it is a variable declaration, or a
	// statement that contains an expression it should be in here.
	switch n := (*node).(type) {
	case *VariableDecl:
		a := v.HandleTyped(n.Pos(), n.Variable)
		if n.Assignment != nil {
			if n.Variable.Type != nil {
				// Slightly hacky, but gets the job done
				n.Assignment.SetType(n.Variable.Type)
			}

			b := v.HandleExpr(n.Assignment)
			v.AddEqualsConstraint(a, b)
		}

	case *AssignStat:
		a := v.HandleExpr(n.Access)
		b := v.HandleExpr(n.Assignment)
		v.AddEqualsConstraint(a, b)

	case *BinopAssignStat:
		a := v.HandleExpr(n.Access)
		b := v.HandleExpr(n.Assignment)
		v.AddEqualsConstraint(a, b)

	case *CallStat:
		v.HandleExpr(n.Call)

	case *DeferStat:
		v.HandleExpr(n.Call)

	case *IfStat:
		for _, expr := range n.Exprs {
			id := v.HandleExpr(expr)
			v.AddIsConstraint(id, PRIMITIVE_bool)
		}

	case *ReturnStat:
		if n.Value != nil {
			id := v.HandleExpr(n.Value)
			v.AddIsConstraint(id, v.Function().Type.Return)
		}

	case *LoopStat:
		if n.Condition != nil {
			id := v.HandleExpr(n.Condition)
			v.AddIsConstraint(id, PRIMITIVE_bool)
		}

	case *MatchStat:
		// TODO: Implement once we actuall do match statement

	}

	return true
}

func (v *Inferrer) HandleExpr(expr Expr) int {
	return v.HandleTyped(expr.Pos(), expr)
}

func (v *Inferrer) HandleTyped(pos lexer.Position, typed Typed) int {
	// If we have already handled this type, return now.
	if ann, ok := v.TypedLookup[typed]; ok {
		return ann.Id
	}

	// Wrap and store the typed so we can access it later
	ann := &AnnotatedTyped{Pos: pos, Id: v.IdCount, Typed: typed}
	v.Typeds[ann.Id] = ann
	v.TypedLookup[typed] = ann
	v.IdCount++

	// Switch on the type of the typed. If it is a `Variable`, any expression,
	// or a literal of some sort, it should be handled here.
	switch typed := typed.(type) {
	case *BinaryExpr:
		a := v.HandleExpr(typed.Lhand)
		b := v.HandleExpr(typed.Rhand)
		switch typed.Op {

		// If we're dealing with a comparison operation, we know that both
		// sides must be of the same type, and that the result will be a bool
		case BINOP_EQ, BINOP_NOT_EQ, BINOP_GREATER, BINOP_LESS,
			BINOP_GREATER_EQ, BINOP_LESS_EQ:
			v.AddEqualsConstraint(a, b)
			v.AddIsConstraint(ann.Id, PRIMITIVE_bool)

		// If we're dealing with bitwise and, or and xor we know that both
		// sides must be the same type, and that the result will be of that
		// type aswell.
		case BINOP_BIT_AND, BINOP_BIT_OR, BINOP_BIT_XOR:
			v.AddEqualsConstraint(a, b)
			v.AddEqualsConstraint(ann.Id, a)

		// If we're dealing with an arithmetic operation we know that both
		// sides must be of the same type, and that the result will be of that
		// type aswell.
		// TODO: These assumptions don't hold once we add operator overloading
		case BINOP_ADD, BINOP_SUB, BINOP_MUL, BINOP_DIV, BINOP_MOD:
			v.AddEqualsConstraint(a, b)
			v.AddEqualsConstraint(ann.Id, a)

		// If we're dealing with a bit shift, we know that the result will be
		// of the same type as the left hand side (the value being shifted).
		case BINOP_BIT_LEFT, BINOP_BIT_RIGHT:
			v.AddEqualsConstraint(ann.Id, a)

		// If we're dealing with a logical operation, we know that both sides
		// must be booleans, and that the result will also be a boolean.
		case BINOP_LOG_AND, BINOP_LOG_OR:
			v.AddIsConstraint(a, PRIMITIVE_bool)
			v.AddIsConstraint(b, PRIMITIVE_bool)
			v.AddIsConstraint(ann.Id, PRIMITIVE_bool)

		default:
			panic("Unhandled binary operator in type inference")

		}

	case *UnaryExpr:
		id := v.HandleExpr(typed.Expr)
		switch typed.Op {
		// If we're dealing with a logical not the expression being not'ed must
		// be a boolean, and the resul will also be a boolean.
		case UNOP_LOG_NOT:
			v.AddIsConstraint(id, PRIMITIVE_bool)
			v.AddIsConstraint(ann.Id, PRIMITIVE_bool)

		// If we're dealing with a bitwise not, the type will be the same type
		// as the expression acted upon.
		case UNOP_BIT_NOT:
			v.AddEqualsConstraint(ann.Id, id)

		// If we're dealing with a arithmetic negation, the type will be the
		// same type as the expression acted upon.
		case UNOP_NEGATIVE:
			v.AddEqualsConstraint(ann.Id, id)

		}

	case *CallExpr:
		if typed.ReceiverAccess != nil {
			v.HandleExpr(typed.ReceiverAccess)
		}

		fnId := v.HandleExpr(typed.Function)
		argIds := make([]int, len(typed.Arguments))
		for idx, arg := range typed.Arguments {
			argIds[idx] = v.HandleExpr(arg)
		}

		// Construct a function type containing the generated type variables.
		// This will be used to infer the types of the arguments.
		fnType := FunctionType{Return: &TypeVariable{Id: ann.Id}}
		for _, argId := range argIds {
			fnType.Parameters = append(fnType.Parameters, &TypeVariable{Id: argId})
		}
		v.AddIsConstraint(fnId, fnType)

	// The type of a cast will always be the type casted to.
	case *CastExpr:
		v.HandleExpr(typed.Expr)
		v.AddIsConstraint(ann.Id, typed.Type)

	// Given an address of expr, we know that the result will be a pointer to
	// the type of the access of which we took the address.
	case *AddressOfExpr:
		id := v.HandleExpr(typed.Access)
		v.AddIsConstraint(ann.Id, PointerTo(&TypeVariable{Id: id}))

	// Given a deref, we know that the expression being dereferenced must be a
	// pointer to the result of the dereference.
	case *DerefAccessExpr:
		id := v.HandleExpr(typed.Expr)
		v.AddIsConstraint(id, PointerTo(&TypeVariable{Id: ann.Id}))

	// A sizeof expr always return a uint
	case *SizeofExpr:
		if typed.Expr != nil {
			v.HandleExpr(typed.Expr)
		}
		v.AddIsConstraint(ann.Id, PRIMITIVE_uint)

	// Given a variable access, we know that the type of the access must be
	// equal to the type of the variable being accessed.
	case *VariableAccessExpr:
		id := v.HandleTyped(typed.Pos(), typed.Variable)
		v.AddEqualsConstraint(ann.Id, id)
		if typed.Variable.Type != nil {
			v.AddIsConstraint(ann.Id, typed.Variable.Type)
		}

	// Given a struct access we generate a constructor type. This type is used
	// because inferring an order sensitive type is not practically possible,
	// without a bit of jerry-rigging.
	case *StructAccessExpr:
		id := v.HandleExpr(typed.Struct)
		v.AddIsConstraint(ann.Id, &ConstructorType{
			Id:   ConstructorStructMember,
			Args: []Type{&TypeVariable{Id: id}},
			Data: typed.Member,
		})

	// Given a struct access we generate a constructor type. This type is used
	// because inferring an order sensitive type is not practically possible,
	// without a bit of jerry-rigging.
	// This one however, will go away once we decomission tuple acceses in
	// favor of tuple destructuring.
	case *TupleAccessExpr:
		id := v.HandleExpr(typed.Tuple)
		v.AddIsConstraint(ann.Id, &ConstructorType{
			Id:   ConstructorTupleIndex,
			Args: []Type{&TypeVariable{Id: id}},
			Data: typed.Index,
		})

	// Given an array access, we know that the type of the expression being
	// accessed must be an array of the same type as the resulting element.
	case *ArrayAccessExpr:
		id := v.HandleExpr(typed.Array)
		v.HandleExpr(typed.Subscript)
		v.AddIsConstraint(id, ArrayOf(&TypeVariable{Id: ann.Id}))

	// An array length expression is always of type uint
	case *ArrayLenExpr:
		v.HandleExpr(typed.Expr)
		v.AddIsConstraint(ann.Id, PRIMITIVE_uint)

	// An enum literal must always come with a type, so we simply bind its type
	// to it's type variable and to the variable from the contained literal
	case *EnumLiteral:
		if typed.Type == nil {
			panic("INTERNAL ERROR: Encountered enum literal without a type")
		}

		id := -1
		if typed.TupleLiteral != nil {
			id = v.HandleExpr(typed.TupleLiteral)
		} else if typed.CompositeLiteral != nil {
			id = v.HandleExpr(typed.CompositeLiteral)
		}
		if id != -1 {
			v.AddIsConstraint(id, typed.Type)
		}
		v.AddIsConstraint(ann.Id, typed.Type)

	// A bool literal will always be of type bool
	case *BoolLiteral:
		v.AddIsConstraint(ann.Id, PRIMITIVE_bool)

	// A string literal will either be of type ^u8 or string respectively
	// depending on whether or not the string is a c-style string.
	case *StringLiteral:
		if typed.IsCString {
			v.AddIsConstraint(ann.Id, PointerTo(PRIMITIVE_u8))
		} else {
			v.AddIsConstraint(ann.Id, stringType)
		}

	// A rune literal will always be of type rune
	case *RuneLiteral:
		v.AddIsConstraint(ann.Id, PRIMITIVE_rune)

	// A composite literal is a mess to handle as it can be either an array or
	// a struct, but in either case we go through and generate the type
	// variables for the contained expression, and if we know the type of the
	// literal we bind the generated type variables to their respective types.
	case *CompositeLiteral:
		ids := make([]int, len(typed.Values))
		for idx, mem := range typed.Values {
			ids[idx] = v.HandleExpr(mem)
		}

		typ := typed.Type.ActualType()
		if at, ok := typ.(ArrayType); ok {
			for _, id := range ids {
				v.AddIsConstraint(id, at.MemberType)
			}
		} else if st, ok := typ.(StructType); ok {
			for idx, id := range ids {
				field := typed.Fields[idx]
				mem := st.GetMember(field)
				v.AddIsConstraint(id, mem.Type)
			}
		}

		if typed.Type != nil {
			v.AddIsConstraint(ann.Id, typed.Type)
		}

	// Given a tuple literal we handle each member, and if we know the type of
	// the tuple we bind their types to their type variables.
	case *TupleLiteral:
		var tt TupleType
		var ok bool
		if typed.Type != nil {
			tt, ok = typed.Type.(TupleType)
		}

		nt := make([]Type, len(typed.Members))
		for idx, mem := range typed.Members {
			id := v.HandleExpr(mem)
			nt[idx] = &TypeVariable{Id: id}
			if ok {
				v.AddIsConstraint(id, tt.Members[idx])
				nt[idx] = tt.Members[idx]
			}
		}

		if typed.Type != nil {
			v.AddIsConstraint(ann.Id, typed.Type)
		} else {
			v.AddIsConstraint(ann.Id, tupleOf(nt...))
		}

	// Given a variable, we bind it's type variable to it's type if its type is known
	case *Variable:
		if typed.GetType() != nil {
			v.AddIsConstraint(ann.Id, typed.GetType())
		}

	// A function access will always be the type of the function it accesses
	case *FunctionAccessExpr:
		v.AddIsConstraint(ann.Id, typed.Function.Type)

	// A lambda expr will always be the type of the function it is
	case *LambdaExpr:
		v.AddIsConstraint(ann.Id, typed.Function.Type)

	// Numeric literals do not get to have any fun, because default types do
	// not mesh well with the constraint based approach.
	case *NumericLiteral:
		// noop

	default:
		panic("INTERNAL ERROR: Unhandled Typed type: " + reflect.TypeOf(typed).String())
	}

	return ann.Id
}

// Unify solves all the added constraints
func (v *Inferrer) Unify() []*Constraint {
	// Create a stack, and copy all constraints to this stack
	stack := make([]*Constraint, len(v.Constraints))
	copy(stack, v.Constraints)

	// Create an array to hold all the final substitutions
	var substitutions []*Constraint

	// subsAll runs the substitues a given id for a new side, on all
	// constraints, both on the stack and in the final substitutions
	subsAll := func(id int, what Side) {
		for idx, cons := range stack {
			stack[idx] = cons.Subs(id, what)
		}
		for idx, cons := range substitutions {
			substitutions[idx] = cons.Subs(id, what)
		}
	}

	// As long as we have a constraint on the stack
	for len(stack) > 0 {
		// Remove a constraint X = Y from the stack
		element := stack[0]
		stack[0], stack = nil, stack[1:]
		x, y := element.Left, element.Right

		// 1. If X and Y are identical identifiers, do nothing.
		if x.SideType == IdentSide && y.SideType == IdentSide && x.Id == y.Id {
			continue
		}

		// 2. If X is an identifier, replace all occurrences of X by Y both on
		// the stack and in the substitution, and add X → Y to the substitution.
		if x.SideType == IdentSide {
			subsAll(x.Id, y)
			substitutions = append(substitutions, &Constraint{
				Left: x, Right: y,
			})
			continue
		}

		// 3. If Y is an identifier, replace all occurrences of Y by X both on
		// the stack and in the substitution, and add Y → X to the substitution.
		if y.SideType == IdentSide {
			subsAll(y.Id, x)
			substitutions = append(substitutions, &Constraint{Left: y, Right: x})
			continue
		}

		// 4. If X is of the form C(X_1, ..., X_n) for some constructor C, and
		// Y is of the form C(Y_1, ..., Y_n) (i.e., it has the same constructor),
		// then push X_i = Y_i for all 1 ≤ i ≤ n onto the stack.

		// 4.0.1. Equal types
		if x.SideType == TypeSide && y.SideType == TypeSide {
			xtyp := x.Type.ActualType()
			ytyp := y.Type.ActualType()
			if xtyp.Equals(ytyp) {
				continue
			}

		}

		// 4.1. {^, &mut, &}x = {^, &mut, &}y
		if x.SideType == TypeSide && y.SideType == TypeSide {
			xAddressee := getAdressee(x.Type)
			yAddressee := getAdressee(y.Type)
			if xAddressee != nil && yAddressee != nil {
				stack = append(stack, ConstraintFromTypes(xAddressee, yAddressee))
				continue
			}
		}

		// 4.2. []x = []y
		if x.SideType == TypeSide && y.SideType == TypeSide {
			atX, okX := x.Type.ActualType().(ArrayType)
			atY, okY := y.Type.ActualType().(ArrayType)
			if okX && okY {
				stack = append(stack, ConstraintFromTypes(atX.MemberType, atY.MemberType))
				continue
			}
		}

		// 4.3 C(x1, ..., xn).d = C(y1, ... yn).d
		// NOTE: This currently handles both struct members and tuple members
		if x.SideType == TypeSide && y.SideType == TypeSide {
			conX, okX := x.Type.(*ConstructorType)
			conY, okY := y.Type.(*ConstructorType)
			if okX && okY && conX.Id == conY.Id && len(conX.Args) == len(conY.Args) &&
				conX.Data == conY.Data {
				for idx, argX := range conX.Args {
					argY := conY.Args[idx]
					stack = append(stack, ConstraintFromTypes(argX, argY))
				}
				continue
			}
		}

		// 4.4. fn(x1, ...) -> xn = fn(y1, ...) -> yn
		if x.SideType == TypeSide && y.SideType == TypeSide {
			xFunc, okX := x.Type.ActualType().(FunctionType)
			yFunc, okY := y.Type.ActualType().(FunctionType)

			if okX && okY {
				// Determine minimum parameter list length.
				// This is done to avoid problems with variadic arguments.
				ln := len(xFunc.Parameters)
				if len(yFunc.Parameters) < ln {
					ln = len(yFunc.Parameters)
				}

				// Parameters
				for idx := 0; idx < ln; idx++ {
					stack = append(stack,
						ConstraintFromTypes(xFunc.Parameters[idx], yFunc.Parameters[idx]))
				}

				// Return type
				xRet := xFunc.Return
				yRet := yFunc.Return
				if xRet == nil {
					xRet = PRIMITIVE_void
				}
				if yRet == nil {
					yRet = PRIMITIVE_void
				}

				stack = append(stack, ConstraintFromTypes(xRet, yRet))
				continue
			}
		}

		// 4.5. (x1, ..., xn) = (y1, ..., yn)
		if x.SideType == TypeSide && y.SideType == TypeSide {
			xTup, okX := x.Type.ActualType().(TupleType)
			yTup, okY := y.Type.ActualType().(TupleType)

			if okX && okY && len(xTup.Members) == len(yTup.Members) {
				for idx, memX := range xTup.Members {
					memY := yTup.Members[idx]
					stack = append(stack, ConstraintFromTypes(memX, memY))
				}
				continue
			}
		}

		// 5. Otherwise, X and Y do not unify. Report an error.
		// NOTE: We defer handling error until the semantic type check
		// TODO: Verify if continuing is ok, or if we should return now
	}

	return substitutions
}

// Finalize runs the actual unification, sets default types in cases where
// these are needed, and sets the inferred types on the expressions.
func (v *Inferrer) Finalize() {
	substitutions := v.Unify()

	// Map all substitutions to the id they act upon
	subList := make([]*Constraint, v.IdCount)
	for _, subs := range substitutions {
		if subs.Left.SideType != IdentSide {
			panic("INTERNAL ERROR: Left side of substitution was not ident")
		}
		ann := v.Typeds[subs.Left.Id]
		subList[ann.Id] = subs
	}

	// Check wither we managed to infer all type
	resolved := true
	for _, val := range subList {
		resolved = resolved && (val == nil || val.Right.SideType != TypeSide)
	}

	// If we didn't manage to infer all the types in the first pass, transfer
	// all the substitutions to the constraint list, and add default types for
	// expression that have these
	if !resolved {
		v.Constraints = nil
		for idx := 0; idx < v.IdCount; idx++ {
			ann := v.Typeds[idx]
			subs := subList[idx]
			if subs != nil && subs.Right.SideType == TypeSide {
				v.AddConstraint(subs)
				continue
			}

			if lit, ok := ann.Typed.(*NumericLiteral); ok {
				typ := PRIMITIVE_int
				if lit.IsFloat {
					typ = PRIMITIVE_f32
					switch lit.floatSizeHint {
					case 'f':
						typ = PRIMITIVE_f32
					case 'd':
						typ = PRIMITIVE_f64
					case 'q':
						typ = PRIMITIVE_f128
					}

				}
				v.AddIsConstraint(idx, typ)
			} else if subs != nil {
				v.AddConstraint(subs)
			}
		}

		// Unify the new constraints
		substitutions = v.Unify()
	}

	// Apply all substitutions
	for _, subs := range substitutions {
		if subs.Left.SideType != IdentSide {
			panic("INTERNAL ERROR: Left side of substitution was not ident")
		}

		ann := v.Typeds[subs.Left.Id]
		if subs.Right.SideType != TypeSide {
			v.errPos(ann.Pos, "Couldn't infer type of expression")
		}

		if _, ok := subs.Right.Type.(*ConstructorType); ok {
			panic("INTERNAL ERROR: ConstructorType escaped inference pass")
		}

		// Set the type of the expression
		ann.Typed.SetType(subs.Right.Type)
	}

	// Type specific touch ups. Here go all the hacky things that was handled
	// in the old inferrence pass, and some new additions to deal with default
	// types.
	for idx := 0; idx < v.IdCount; idx++ {
		ann := v.Typeds[idx]

		switch n := ann.Typed.(type) {
		case *CallExpr:
			// If the function source is a struct access, resolve the method
			// this access represents.
			if sae, ok := n.Function.(*StructAccessExpr); ok {
				fn := TypeWithoutPointers(sae.Struct.GetType()).(*NamedType).GetMethod(sae.Member)
				n.Function = &FunctionAccessExpr{Function: fn}
				if n.Function == nil {
					v.errPos(sae.Pos(), "Type `%s` has no method `%s`", TypeWithoutPointers(sae.Struct.GetType()).TypeName(), sae.Member)
				}
			}

			if n.Function != nil {
				if _, ok := n.Function.GetType().(FunctionType); !ok {
					v.errPos(n.Function.Pos(), "Attempt to call non-function `%s`", n.Function.GetType().TypeName())
				}

				// Insert a deref in cases where the code tries to call a value reciver
				// with a pointer type.
				if n.Function.GetType().(FunctionType).Receiver != nil {
					recType := n.Function.GetType().(FunctionType).Receiver
					accessType := n.ReceiverAccess.GetType()

					if accessType.LevelsOfIndirection() == recType.LevelsOfIndirection()+1 {
						n.ReceiverAccess = &DerefAccessExpr{Expr: n.ReceiverAccess}
					}
				}
			}

		case *StructAccessExpr:
			// Check if we're dealing with a method and exit early
			baseType := TypeWithoutPointers(n.Struct.GetType())
			if nt, ok := baseType.(*NamedType); ok && nt.GetMethod(n.Member) != nil {
				break
			}

			// Insert a deref in cases where the code tries to access a struct
			// member from a pointer type.
			if n.Struct.GetType().ActualType().LevelsOfIndirection() == 1 {
				n.Struct = &DerefAccessExpr{Expr: n.Struct}
			}

			// Verify that we're actually dealing with a struct.
			typ := n.Struct.GetType()
			structType, ok := typ.ActualType().(StructType)
			if !ok {
				v.errPos(n.Pos(), "Cannot access member of type `%s`", typ.TypeName())
			}

			// Verify that the struct actually has the requested member.
			mem := structType.GetMember(n.Member)
			if mem == nil {
				v.errPos(n.Pos(), "Struct `%s` does not contain member or method `%s`", typ.TypeName(), n.Member)
			}

		case *BinaryExpr:
			nll, ok1 := n.Lhand.(*NumericLiteral)
			nlr, ok2 := n.Rhand.(*NumericLiteral)

			// Here we deal with the case where two numeric literals appear in
			// a binary expression, but where one of them is a float literal
			// and the other isn't.
			if ok1 && ok2 && nll.IsFloat {
				nlr.SetType(nll.GetType())
				break
			}

			if ok1 && ok2 && nlr.IsFloat {
				nll.SetType(nlr.GetType())
				break
			}

		case *CastExpr:
			expr, ok := n.Expr.(*NumericLiteral)

			// Here we handle the case where a numeric literal appear in a cast
			// to a pointer type. We need the default type to be uintptr here
			// as normal integers can't be cast to a pointer.
			if ok && n.Type.LevelsOfIndirection() > 0 {
				expr.SetType(PRIMITIVE_uintptr)
			}
		}
	}
}

// UnaryExpr
func (v *UnaryExpr) SetType(t Type) {
	v.Type = t
}

// BinaryExpr
func (v *BinaryExpr) SetType(t Type) {
	v.Type = t
}

// NumericLiteral
func (v *NumericLiteral) SetType(t Type) {
	var actual Type
	if t != nil {
		actual = t.ActualType()
	}

	if v.IsFloat {
		switch actual {
		case PRIMITIVE_f32, PRIMITIVE_f64, PRIMITIVE_f128:
			v.Type = t

		default:
			v.Type = PRIMITIVE_f64
		}
	} else {
		switch actual {
		case PRIMITIVE_int, PRIMITIVE_uint, PRIMITIVE_uintptr,
			PRIMITIVE_s8, PRIMITIVE_s16, PRIMITIVE_s32, PRIMITIVE_s64, PRIMITIVE_s128,
			PRIMITIVE_u8, PRIMITIVE_u16, PRIMITIVE_u32, PRIMITIVE_u64, PRIMITIVE_u128,
			PRIMITIVE_f32, PRIMITIVE_f64, PRIMITIVE_f128:
			v.Type = t

		default:
			v.Type = PRIMITIVE_int
		}
	}
}

// ArrayLiteral
func (v *CompositeLiteral) SetType(t Type) {
	if t == nil {
		return
	}

	switch t.ActualType().(type) {
	case StructType, ArrayType:
		v.Type = t
	}
}

// StringLiteral
func (v *StringLiteral) SetType(t Type) {
	v.Type = t
}

// TupleLiteral
func (v *TupleLiteral) SetType(t Type) {
	if t == nil {
		return
	}

	_, ok := t.ActualType().(TupleType)
	if ok {
		v.Type = t
	}
}

// Variable
func (v *Variable) SetType(t Type) {
	if v.Type == nil {
		v.Type = t
	}
}

// Noops
func (_ AddressOfExpr) SetType(t Type)      {}
func (_ ArrayAccessExpr) SetType(t Type)    {}
func (_ ArrayLenExpr) SetType(t Type)       {}
func (_ BoolLiteral) SetType(t Type)        {}
func (_ CastExpr) SetType(t Type)           {}
func (_ CallExpr) SetType(t Type)           {}
func (_ DefaultMatchBranch) SetType(t Type) {}
func (_ DerefAccessExpr) SetType(t Type)    {}
func (_ EnumLiteral) SetType(t Type)        {}
func (_ FunctionAccessExpr) SetType(t Type) {}
func (_ LambdaExpr) SetType(t Type)         {}
func (_ RuneLiteral) SetType(t Type)        {}
func (_ VariableAccessExpr) SetType(t Type) {}
func (_ SizeofExpr) SetType(t Type)         {}
func (_ StructAccessExpr) SetType(t Type)   {}
func (_ TupleAccessExpr) SetType(t Type)    {}
