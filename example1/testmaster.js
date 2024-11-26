import tarantool from "k6/x/tarantool";
import { check } from "k6";

const conn = tarantool.connect(["localhost:3301"],{
    user: "example_user",
    pass: "example_password",
});

export default () => {
    const id = 1 + Math.floor(Math.random() * (1e6 - 1));
    const result = tarantool.call(conn, "box.space.test:get", [id]);
    check(result, {
        "code is 0": (r) => r.code === 0,
        "id is correct": (r) => {
            if (r.data && r.data[0] && r.data[0][0] === id) 
                return true;
            else {
                console.log(JSON.stringify({id: id, result: r})); 
                return false;
            }
        },
        "id in value is correct": (r) => r.data && r.data[0] && r.data[0][1] && JSON.parse(r.data[0][1]).id === id,
    });
};
